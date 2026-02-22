package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/client"
	"github.com/go-mysql-org/go-mysql/packet"
)

func newClientConnFromNetConn(c net.Conn) *client.Conn {
	return &client.Conn{Conn: packet.NewConn(c)}
}

func TestBorrowReturnsFactoryConnWhenPoolEmpty(t *testing.T) {
	t.Parallel()

	var called bool
	factory := func(context.Context) (*client.Conn, error) {
		called = true
		local, remote := net.Pipe()
		_ = remote.Close()
		return newClientConnFromNetConn(local), nil
	}

	p := NewBackendPool(1, time.Minute, time.Second, slog.Default(), factory)
	defer p.Close()

	conn, err := p.Borrow(context.Background())
	if err != nil {
		t.Fatalf("Borrow returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("Borrow returned nil conn")
	}
	if !called {
		t.Fatal("expected factory to be called")
	}
	_ = conn.Close()
}

func TestBorrowDiscardStaleAndRefill(t *testing.T) {
	t.Parallel()

	refilled := make(chan struct{}, 1)
	factory := func(context.Context) (*client.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			_, _ = io.Copy(io.Discard, remote)
		}()
		select {
		case refilled <- struct{}{}:
		default:
		}
		return newClientConnFromNetConn(local), nil
	}

	p := NewBackendPool(1, 10*time.Millisecond, time.Second, slog.Default(), factory)
	defer p.Close()

	local, remote := net.Pipe()
	_ = remote.Close()
	p.conns <- &PooledConn{
		conn:      newClientConnFromNetConn(local),
		createdAt: time.Now().Add(-time.Hour),
	}

	conn, err := p.Borrow(context.Background())
	if err != nil {
		t.Fatalf("Borrow returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("Borrow returned nil conn")
	}
	_ = conn.Close()

	select {
	case <-refilled:
	case <-time.After(2 * time.Second):
		t.Fatal("expected pool refill to be triggered")
	}
}

func TestFillOneUsesTimeoutContext(t *testing.T) {
	t.Parallel()

	factoryCalled := make(chan time.Duration, 1)
	factory := func(ctx context.Context) (*client.Conn, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected refill context to have deadline")
		}
		factoryCalled <- time.Until(deadline)
		return nil, context.DeadlineExceeded
	}

	p := NewBackendPool(1, time.Minute, 500*time.Millisecond, slog.Default(), factory)
	defer p.Close()

	p.fillOne()

	select {
	case d := <-factoryCalled:
		if d <= 0 || d > time.Second {
			t.Fatalf("unexpected timeout window: %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("factory was not called")
	}
}

func TestBorrowLogsSingleSummaryForStaleConnections(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	factory := func(context.Context) (*client.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			_, _ = io.Copy(io.Discard, remote)
		}()
		return newClientConnFromNetConn(local), nil
	}

	p := NewBackendPool(1, time.Minute, time.Second, logger, factory)
	defer p.Close()

	staleLocal, staleRemote := net.Pipe()
	_ = staleRemote.Close()
	p.conns <- &PooledConn{
		conn:      newClientConnFromNetConn(staleLocal),
		createdAt: time.Now(),
	}

	conn, err := p.Borrow(context.Background())
	if err != nil {
		t.Fatalf("Borrow returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("Borrow returned nil conn")
	}
	_ = conn.Close()

	out := buf.String()
	if !strings.Contains(out, "refreshed stale pooled connections") {
		t.Fatalf("expected stale summary log, got: %s", out)
	}
	if strings.Contains(out, "discarding stale pooled connection") {
		t.Fatalf("did not expect per-connection stale logs at info level, got: %s", out)
	}
}
