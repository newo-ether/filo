package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEventSubscriptionLimitReleasesBeforeNativeAttach(t *testing.T) {
	hub := NewHub()
	leases := make([]*Subscription, 0, maxEventSubscribers)
	defer hub.Close()
	for i := 0; i < maxEventSubscribers; i++ {
		lease, ok := hub.TrySubscribe("session")
		if !ok {
			t.Fatalf("subscription %d was refused", i)
		}
		leases = append(leases, lease)
	}
	source := newFakeSessions(1)
	server := &Server{events: hub, sessions: source}
	if err := server.stream(context.Background(), httptest.NewRecorder(), "session", nil, false); err == nil {
		t.Fatal("over-limit stream was admitted")
	}
	if source.attaches != 0 {
		t.Fatal("refused stream acquired native resources")
	}
	leases[0].Close()
	leases[0].Close()
	if lease, ok := hub.TrySubscribe("other"); !ok {
		t.Fatal("released capacity was not reusable")
	} else {
		lease.Close()
	}
	hub.Reset()
	for i := 0; i < maxEventSubscribers; i++ {
		if _, ok := hub.TrySubscribe("new"); !ok {
			t.Fatal("reset retained old capacity")
		}
	}
}

type failedFlushWriter struct{ *httptest.ResponseRecorder }

func (w failedFlushWriter) FlushError() error { return io.ErrClosedPipe }

func TestStreamFlushFailureReleasesNativeAndSubscriber(t *testing.T) {
	source := newFakeSessions(1)
	hub := NewHub()
	server := &Server{events: hub, sessions: source}
	writer := &tracking{ResponseWriter: newDeadlineResponseWriter(failedFlushWriter{httptest.NewRecorder()}, time.Second)}
	if err := server.stream(context.Background(), writer, "session", nil, false); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("flush failure: %v", err)
	}
	if source.attaches != 1 || source.releases != 1 {
		t.Fatalf("native leases: %d/%d", source.attaches, source.releases)
	}
	if len(hub.subscribers) != 0 {
		t.Fatal("failed stream retained its subscription")
	}
}

// A real socket pair proves a peer that stops reading cannot retain the SSE
// lease. The timeout is injected into the production writer, not the native call.
func TestSlowStreamWriteDeadlineReleasesNativeLease(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	listener := &pipeListener{connection: serverConn, closed: make(chan struct{})}
	source := newFakeSessions(1)
	hub := NewHub()
	application := &Server{events: hub, sessions: source}
	completed := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bounded := newDeadlineResponseWriter(w, 30*time.Millisecond)
		defer bounded.finish()
		completed <- application.stream(r.Context(), bounded, "session", nil, false)
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	if err := clientConn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(clientConn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		var failure net.Error
		if !errors.As(err, &failure) || !failure.Timeout() {
			t.Fatalf("expected write timeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow reader retained the stream")
	}
	if source.attaches != 1 || source.releases != 1 {
		t.Fatalf("native leases: %d/%d", source.attaches, source.releases)
	}
	if len(hub.subscribers) != 0 {
		t.Fatal("slow reader retained its subscription")
	}
}

type pipeListener struct {
	connection net.Conn
	closed     chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	if l.connection != nil {
		connection := l.connection
		l.connection = nil
		return connection, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *pipeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddress{} }

type pipeAddress struct{}

func (pipeAddress) Network() string { return "pipe" }
func (pipeAddress) String() string  { return "pipe" }
