package proxy

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestPipeTransfersDataBothDirections(t *testing.T) {
	t.Parallel()

	p := &Proxy{}

	client, clientPeer := net.Pipe()
	backend, backendPeer := net.Pipe()
	defer clientPeer.Close()
	defer backendPeer.Close()

	done := make(chan struct {
		up, down int64
		err      error
	}, 1)
	go func() {
		up, down, err := p.pipe(client, backend)
		done <- struct {
			up, down int64
			err      error
		}{up: up, down: down, err: err}
	}()

	msgUp := []byte("hello-backend")
	msgDown := []byte("hello-client")

	if _, err := clientPeer.Write(msgUp); err != nil {
		t.Fatalf("write client peer: %v", err)
	}
	gotUp := make([]byte, len(msgUp))
	if _, err := io.ReadFull(backendPeer, gotUp); err != nil {
		t.Fatalf("read backend peer: %v", err)
	}

	if _, err := backendPeer.Write(msgDown); err != nil {
		t.Fatalf("write backend peer: %v", err)
	}
	gotDown := make([]byte, len(msgDown))
	if _, err := io.ReadFull(clientPeer, gotDown); err != nil {
		t.Fatalf("read client peer: %v", err)
	}

	_ = clientPeer.Close()
	_ = backendPeer.Close()

	select {
	case res := <-done:
		if res.err != nil && !isConnCloseErr(res.err) {
			t.Fatalf("pipe returned error: %v", res.err)
		}
		if res.up == 0 || res.down == 0 {
			t.Fatalf("expected bytes in both directions, got up=%d down=%d", res.up, res.down)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipe did not finish")
	}
}

func TestIsConnCloseErr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err  error
		want bool
	}{
		{err: nil, want: false},
		{err: io.EOF, want: true},
		{err: net.ErrClosed, want: true},
		{err: errors.New("write: broken pipe"), want: true},
		{err: errors.New("connection reset by peer"), want: true},
		{err: errors.New("use of closed network connection"), want: true},
		{err: errors.New("unexpected protocol failure"), want: false},
	}

	for _, tc := range cases {
		got := isConnCloseErr(tc.err)
		if got != tc.want {
			t.Fatalf("isConnCloseErr(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestActiveConnectionTrackingAndForceClose(t *testing.T) {
	t.Parallel()

	p := &Proxy{
		active: make(map[uint64]*trackedConn),
	}

	client, clientPeer := net.Pipe()
	backend, backendPeer := net.Pipe()
	defer clientPeer.Close()
	defer backendPeer.Close()

	p.trackClient(42, client, time.Now().Add(-2*time.Second))
	p.trackBackend(42, backend)

	count, oldest := p.activeSummary()
	if count != 1 {
		t.Fatalf("expected active_count=1, got %d", count)
	}
	if oldest <= 0 {
		t.Fatalf("expected oldest age > 0, got %v", oldest)
	}

	forced := p.forceCloseActive()
	if forced != 2 {
		t.Fatalf("expected 2 forced closes (client+backend), got %d", forced)
	}

	if _, err := clientPeer.Write([]byte("x")); err == nil {
		t.Fatal("expected client peer write to fail after force close")
	}
	if _, err := backendPeer.Write([]byte("x")); err == nil {
		t.Fatal("expected backend peer write to fail after force close")
	}
}

func TestUntrackRemovesActiveConnection(t *testing.T) {
	t.Parallel()

	p := &Proxy{
		active: make(map[uint64]*trackedConn),
	}

	client, clientPeer := net.Pipe()
	defer client.Close()
	defer clientPeer.Close()

	p.trackClient(7, client, time.Now())
	p.untrack(7)

	count, oldest := p.activeSummary()
	if count != 0 {
		t.Fatalf("expected active_count=0, got %d", count)
	}
	if oldest != 0 {
		t.Fatalf("expected oldest age = 0, got %v", oldest)
	}
}
