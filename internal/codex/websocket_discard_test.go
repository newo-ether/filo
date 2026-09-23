package codex

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestObserverDrainsLargeFramesAndKeepsSubscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := UpgradeWebSocket(w, r)
		if err != nil {
			return
		}
		defer socket.Terminate()
		_ = socket.WriteText(bytes.Repeat([]byte("x"), 2<<20))
		_ = socket.writeFrame(opPing, []byte("ping"))
		_ = socket.WriteText([]byte("terminal"))
		_, _ = socket.ReadMessage()
	}))
	defer server.Close()
	socket, err := DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Terminate()
	body, err := socket.ReadSmallMessage(1 << 20)
	if err != nil || string(body) != "terminal" {
		t.Fatalf("terminal lost after oversized tool: %q %v", body, err)
	}
}

func TestObserverDrainsFragmentedMessageAtCumulativeLimit(t *testing.T) {
	var raw bytes.Buffer
	// Three 4-byte fragments exceed the 8-byte whole-message budget.
	for _, header := range []byte{0x01, 0x00, 0x80} {
		raw.Write([]byte{header, 4})
		raw.WriteString("1234")
	}
	raw.Write([]byte{0x81, 2})
	raw.WriteString("ok")
	socket := &WebSocket{reader: bufio.NewReader(&raw), client: true, limit: 100}
	body, err := socket.ReadSmallMessage(8)
	if err != nil || string(body) != "ok" {
		t.Fatalf("%q %v", body, err)
	}
}
