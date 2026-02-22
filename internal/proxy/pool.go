package proxy

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-mysql-org/go-mysql/client"
)

type PooledConn struct {
	conn      *client.Conn
	createdAt time.Time
}

type BackendPool struct {
	mu            sync.RWMutex
	closed        bool
	conns         chan *PooledConn
	maxLife       time.Duration
	factory       func(context.Context) (*client.Conn, error)
	logger        *slog.Logger
	refillCtx     context.Context
	refillCancel  context.CancelFunc
	refillTimeout time.Duration
}

func NewBackendPool(size int, maxLife, refillTimeout time.Duration, logger *slog.Logger, factory func(context.Context) (*client.Conn, error)) *BackendPool {
	if size < 1 {
		size = 1
	}
	if refillTimeout <= 0 {
		refillTimeout = 8 * time.Second
	}
	refillCtx, refillCancel := context.WithCancel(context.Background())
	p := &BackendPool{
		conns:         make(chan *PooledConn, size),
		maxLife:       maxLife,
		factory:       factory,
		logger:        logger,
		refillCtx:     refillCtx,
		refillCancel:  refillCancel,
		refillTimeout: refillTimeout,
	}
	return p
}

func (p *BackendPool) Start(ctx context.Context) {
	for i := 0; i < cap(p.conns); i++ {
		go p.fillOne()
	}
}

func (p *BackendPool) Borrow(ctx context.Context) (*client.Conn, error) {
	staleDiscarded := 0
	lastStaleReason := ""

	for {
		select {
		case <-ctx.Done():
			if staleDiscarded > 0 {
				p.logger.Info("refreshed stale pooled connections", "discarded", staleDiscarded, "last_reason", lastStaleReason)
			}
			return nil, ctx.Err()
		case pooled := <-p.conns:
			if pooled == nil {
				if staleDiscarded > 0 {
					p.logger.Info("refreshed stale pooled connections", "discarded", staleDiscarded, "last_reason", lastStaleReason)
				}
				return p.factory(ctx)
			}
			if time.Since(pooled.createdAt) > p.maxLife {
				_ = pooled.conn.Close()
				go p.fillOne()
				continue
			}
			if err := pooled.conn.Ping(); err != nil {
				reason := compactErr(err)
				staleDiscarded++
				lastStaleReason = reason
				p.logger.Debug("discarding stale pooled connection", "reason", reason)
				_ = pooled.conn.Close()
				go p.fillOne()
				continue
			}
			go p.fillOne()
			if staleDiscarded > 0 {
				p.logger.Info("refreshed stale pooled connections", "discarded", staleDiscarded, "last_reason", lastStaleReason)
			}
			return pooled.conn, nil
		default:
			if staleDiscarded > 0 {
				p.logger.Info("refreshed stale pooled connections", "discarded", staleDiscarded, "last_reason", lastStaleReason)
			}
			return p.factory(ctx)
		}
	}
}

func (p *BackendPool) fillOne() {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return
	}
	p.mu.RUnlock()

	ctx, cancel := context.WithTimeout(p.refillCtx, p.refillTimeout)
	defer cancel()

	conn, err := p.factory(ctx)
	if err != nil {
		p.logger.Warn("pool prewarm failed", "reason", compactErr(err))
		return
	}

	item := &PooledConn{
		conn:      conn,
		createdAt: time.Now(),
	}

	select {
	case p.conns <- item:
	default:
		_ = conn.Close()
	}
}

func (p *BackendPool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.refillCancel()
	p.mu.Unlock()

	for {
		select {
		case c := <-p.conns:
			if c != nil && c.conn != nil {
				_ = c.conn.Close()
			}
		default:
			return
		}
	}
}

func compactErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}
