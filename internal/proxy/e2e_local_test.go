package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"rds-iam-proxy/internal/config"

	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
)

func TestLocalOnlyEndToEndProxyFlow(t *testing.T) {
	t.Parallel()

	backendAddr := freeTCPAddr(t)
	proxyAddr := freeTCPAddr(t)

	backendUser := "backend_user"
	backendPass := "backend_pass"

	backendStop := startFakeBackend(t, backendAddr, backendUser, backendPass)
	defer backendStop()

	profile := config.Profile{
		Name:          "e2e-local",
		ListenAddr:    proxyAddr,
		MaxConns:      10,
		ProxyUser:     "local_proxy_e2e",
		ProxyPassword: "local_proxy_pass",
		RDSHost:       "local-backend",
		RDSPort:       3306,
		RDSRegion:     "eu-west-1",
		RDSDBUser:     "ignored-in-local-e2e",
		CABundle:      "/tmp/unused-in-local-e2e.pem",
	}

	pool := NewBackendPool(2, time.Minute, time.Second, slog.Default(), func(ctx context.Context) (*client.Conn, error) {
		return client.ConnectWithContext(ctx, backendAddr, backendUser, backendPass, "", 2*time.Second, func(c *client.Conn) error {
			c.UnsetCapability(mysql.CLIENT_QUERY_ATTRIBUTES)
			c.UnsetCapability(mysql.CLIENT_COMPRESS)
			c.UnsetCapability(mysql.CLIENT_ZSTD_COMPRESSION_ALGORITHM)
			return nil
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	proxy := New(profile, slog.Default(), pool, 5*time.Second, 20)
	runErr := make(chan error, 1)
	go func() {
		runErr <- proxy.Run(ctx)
	}()

	waitForTCP(t, proxyAddr, 3*time.Second)

	frontend, err := client.Connect(proxyAddr, profile.ProxyUser, profile.ProxyPassword, "")
	if err != nil {
		t.Fatalf("connect frontend->proxy: %v", err)
	}
	defer frontend.Close()

	result, err := frontend.Execute("SELECT 1")
	if err != nil {
		t.Fatalf("execute query through proxy: %v", err)
	}
	if !result.HasResultset() || result.RowNumber() != 1 {
		t.Fatalf("unexpected resultset shape")
	}
	got, err := result.GetInt(0, 0)
	if err != nil {
		t.Fatalf("read result value: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}

	_ = frontend.Close()
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("proxy run error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not shut down")
	}
}

func startFakeBackend(t *testing.T, addr, user, pass string) func() {
	t.Helper()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen fake backend: %v", err)
	}

	stopCh := make(chan struct{})
	go func() {
		<-stopCh
		_ = ln.Close()
	}()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			go handleFakeBackendConn(conn, user, pass)
		}
	}()

	return func() {
		close(stopCh)
	}
}

func handleFakeBackendConn(conn net.Conn, user, pass string) {
	defer conn.Close()

	handler := fakeBackendHandler{}
	srvConn, err := server.NewConn(conn, user, pass, handler)
	if err != nil {
		return
	}
	for {
		if err := srvConn.HandleCommand(); err != nil {
			if err == io.EOF || strings.Contains(err.Error(), "use of closed network connection") {
				return
			}
			return
		}
	}
}

type fakeBackendHandler struct {
	server.EmptyHandler
}

func (fakeBackendHandler) HandleQuery(query string) (*mysql.Result, error) {
	q := strings.TrimSpace(strings.ToUpper(query))
	switch q {
	case "SELECT 1", "SELECT 1;":
		rs, err := mysql.BuildSimpleTextResultset([]string{"1"}, [][]interface{}{{1}})
		if err != nil {
			return nil, err
		}
		return mysql.NewResult(rs), nil
	default:
		return nil, mysql.NewError(mysql.ER_UNKNOWN_ERROR, "unsupported query in local e2e backend")
	}
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeTCPAddr listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForTCP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tcp listener at %s", addr)
}
