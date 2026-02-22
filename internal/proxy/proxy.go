package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rds-iam-proxy/internal/config"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
)

type Proxy struct {
	profile         config.Profile
	logger          *slog.Logger
	pool            *BackendPool
	shutdownTimeout time.Duration
	maxConns        int
	sem             chan struct{}
	nextConnID      atomic.Uint64
	activeMu        sync.RWMutex
	active          map[uint64]*trackedConn
	ln              net.Listener
	wg              sync.WaitGroup
}

type trackedConn struct {
	client    net.Conn
	backend   net.Conn
	startedAt time.Time
}

func New(p config.Profile, logger *slog.Logger, pool *BackendPool, shutdownTimeout time.Duration, maxConns int) *Proxy {
	if maxConns <= 0 {
		maxConns = 200
	}
	return &Proxy{
		profile:         p,
		logger:          logger,
		pool:            pool,
		shutdownTimeout: shutdownTimeout,
		maxConns:        maxConns,
		sem:             make(chan struct{}, maxConns),
		active:          make(map[uint64]*trackedConn),
	}
}

func (p *Proxy) Run(ctx context.Context) error {
	defer p.pool.Close()

	ln, err := net.Listen("tcp", p.profile.ListenAddr)
	if err != nil {
		return err
	}
	p.ln = ln
	p.logger.Info("proxy listening", "listen_addr", p.profile.ListenAddr, "rds_host", p.profile.RDSHost, "rds_port", p.profile.RDSPort, "max_conns", p.maxConns)

	go func() {
		<-ctx.Done()
		_ = p.ln.Close()
	}()

	for {
		conn, err := p.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				break
			}
			p.logger.Warn("accept failed", "error", err)
			continue
		}

		select {
		case p.sem <- struct{}{}:
		case <-ctx.Done():
			_ = conn.Close()
			return nil
		}

		connID := p.nextConnID.Add(1)
		p.wg.Add(1)
		go func(c net.Conn, id uint64) {
			defer p.wg.Done()
			defer func() { <-p.sem }()
			p.handleConn(ctx, c, id)
		}(conn, connID)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.wg.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(p.shutdownTimeout):
		activeCount, oldestAge := p.activeSummary()
		forced := p.forceCloseActive()
		p.logger.Warn(
			"shutdown timeout hit; forcing active connection close",
			"active_count", activeCount,
			"oldest_age_ms", oldestAge.Milliseconds(),
			"forced_closes", forced,
		)
		select {
		case <-done:
			return nil
		case <-time.After(2 * time.Second):
			return nil
		}
	}
}

func (p *Proxy) handleConn(ctx context.Context, clientConn net.Conn, connID uint64) {
	startedAt := time.Now()
	p.trackClient(connID, clientConn, startedAt)
	defer p.untrack(connID)

	log := p.logger.With("conn_id", connID, "remote_addr", clientConn.RemoteAddr().String())
	log.Info("connection accepted")
	defer clientConn.Close()
	defer func() {
		log.Info("connection closed", "duration_ms", time.Since(startedAt).Milliseconds())
	}()

	serverConn, err := authenticateClient(clientConn, p.profile)
	if err != nil {
		log.Warn("client auth failed", "error", err)
		return
	}

	backendConn, err := p.pool.Borrow(ctx)
	if err != nil {
		log.Error("backend unavailable", "error", err)
		respondBackendUnavailable(serverConn)
		return
	}
	defer backendConn.Close() // single-use by design
	p.trackBackend(connID, backendConn.Conn)

	log.Debug("backend connection acquired")

	up, down, pipeErr := p.pipe(serverConn.Conn, backendConn.Conn)
	if pipeErr != nil {
		log.Warn("pipe ended with error", "error", pipeErr, "bytes_up", up, "bytes_down", down)
		return
	}
	log.Info("pipe finished", "bytes_up", up, "bytes_down", down)
}

func (p *Proxy) pipe(client net.Conn, backend net.Conn) (int64, int64, error) {
	type copyResult struct {
		n   int64
		err error
	}
	resCh := make(chan copyResult, 2)

	go func() {
		n, err := io.Copy(backend, client)
		resCh <- copyResult{n: n, err: err}
	}()

	go func() {
		n, err := io.Copy(client, backend)
		resCh <- copyResult{n: n, err: err}
	}()

	first := <-resCh
	_ = client.Close()
	_ = backend.Close()
	second := <-resCh

	up := first.n
	down := second.n

	if first.err != nil && !isConnCloseErr(first.err) {
		return up, down, first.err
	}
	if second.err != nil && !isConnCloseErr(second.err) {
		return up, down, second.err
	}

	return up, down, nil
}

func writeErrPacket(conn *server.Conn, code uint16, msg string) error {
	if msg == "" {
		msg = "backend unavailable"
	}
	data := make([]byte, 4, 16+len(msg))
	data = append(data, mysql.ERR_HEADER)
	data = append(data, byte(code), byte(code>>8))
	data = append(data, '#')
	data = append(data, []byte("HY000")...)
	data = append(data, msg...)
	return conn.WritePacket(data)
}

func respondBackendUnavailable(conn *server.Conn) {
	// Best-effort protocol-correct error response:
	// wait for one client command packet, then reply with ERR.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	if _, err := conn.ReadPacket(); err != nil {
		return
	}
	_ = writeErrPacket(conn, mysql.ER_CON_COUNT_ERROR, "backend unavailable")
}

func isConnCloseErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
		return true
	}
	// Some MySQL clients trigger connection resets on quit.
	return strings.Contains(err.Error(), "connection reset by peer") ||
		strings.Contains(err.Error(), "broken pipe") ||
		strings.Contains(err.Error(), "read/write on closed pipe") ||
		isUseOfClosedConn(err)
}

func isUseOfClosedConn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}

func (p *Proxy) trackClient(connID uint64, client net.Conn, startedAt time.Time) {
	p.activeMu.Lock()
	p.active[connID] = &trackedConn{
		client:    client,
		startedAt: startedAt,
	}
	p.activeMu.Unlock()
}

func (p *Proxy) trackBackend(connID uint64, backend net.Conn) {
	p.activeMu.Lock()
	if tc, ok := p.active[connID]; ok {
		tc.backend = backend
	}
	p.activeMu.Unlock()
}

func (p *Proxy) untrack(connID uint64) {
	p.activeMu.Lock()
	delete(p.active, connID)
	p.activeMu.Unlock()
}

func (p *Proxy) activeSummary() (count int, oldestAge time.Duration) {
	now := time.Now()
	p.activeMu.RLock()
	defer p.activeMu.RUnlock()
	count = len(p.active)
	for _, tc := range p.active {
		age := now.Sub(tc.startedAt)
		if age > oldestAge {
			oldestAge = age
		}
	}
	return count, oldestAge
}

func (p *Proxy) forceCloseActive() int {
	type pair struct {
		client  net.Conn
		backend net.Conn
	}

	p.activeMu.RLock()
	pairs := make([]pair, 0, len(p.active))
	for _, tc := range p.active {
		pairs = append(pairs, pair{client: tc.client, backend: tc.backend})
	}
	p.activeMu.RUnlock()

	closed := 0
	for _, item := range pairs {
		if item.client != nil {
			_ = item.client.Close()
			closed++
		}
		if item.backend != nil {
			_ = item.backend.Close()
			closed++
		}
	}
	return closed
}
