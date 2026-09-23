package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

// hostPeer is a minimal native owner: it upgrades a WebSocket and speaks one
// JSON packet per message, exactly as the real app server does.
type hostPeer struct {
	server      *httptest.Server
	connections atomic.Int64
}

func startHostPeer(t *testing.T, handle func(number int, socket *WebSocket, authorization string)) *hostPeer {
	t.Helper()
	peer := &hostPeer{}
	peer.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		socket, err := UpgradeWebSocket(writer, request)
		if err != nil {
			return
		}
		defer socket.Terminate()
		handle(int(peer.connections.Add(1)), socket, request.Header.Get("Authorization"))
	}))
	t.Cleanup(peer.server.Close)
	return peer
}

func (peer *hostPeer) url() string {
	return "ws" + strings.TrimPrefix(peer.server.URL, "http")
}

func (peer *hostPeer) count() int { return int(peer.connections.Load()) }

// readPacket reads one client packet and reports a transport failure, which is
// only legal once the client itself has stopped talking.
func readPacket(t *testing.T, socket *WebSocket) (map[string]any, bool) {
	t.Helper()
	message, err := socket.ReadMessage()
	if err != nil {
		t.Errorf("the peer failed to read a packet: %v", err)
		return nil, false
	}
	return decodePacket(t, message)
}

// readPacketUntilClose reads one packet and treats a client disconnect as the
// end of the peer's work, since the client closes the socket when it finishes.
func readPacketUntilClose(t *testing.T, socket *WebSocket) (map[string]any, bool) {
	t.Helper()
	message, err := socket.ReadMessage()
	if err != nil {
		return nil, false
	}
	return decodePacket(t, message)
}

// decodePacket enforces the transport expectation that every packet arrives as
// one message with no trailing line ending.
func decodePacket(t *testing.T, message []byte) (map[string]any, bool) {
	t.Helper()
	if strings.HasSuffix(string(message), "\n") || strings.HasSuffix(string(message), "\r") {
		t.Errorf("the client sent a trailing line ending: %q", message)
		return nil, false
	}
	var packet map[string]any
	if err := json.Unmarshal(message, &packet); err != nil {
		t.Errorf("the peer received %q: %v", message, err)
		return nil, false
	}
	return packet, true
}

func writePacket(t *testing.T, socket *WebSocket, packet map[string]any) bool {
	t.Helper()
	payload, err := json.Marshal(packet)
	if err != nil {
		t.Errorf("marshal a peer packet: %v", err)
		return false
	}
	if err := socket.WriteText(payload); err != nil {
		t.Errorf("write a peer packet: %v", err)
		return false
	}
	return true
}

func answerRequest(t *testing.T, socket *WebSocket, packet map[string]any) bool {
	t.Helper()
	identifier, hasIdentifier := packet["id"]
	if !hasIdentifier {
		t.Errorf("the peer expected a request, received %#v", packet)
		return false
	}
	return writePacket(t, socket, map[string]any{"id": identifier, "result": map[string]any{}})
}

func method(t *testing.T, packet map[string]any) string {
	t.Helper()
	name, _ := packet["method"].(string)
	return name
}

func TestConnectCodexHostKeepsAStallInsideItsBudget(t *testing.T) {
	requests := make(chan map[string]any, 8)
	peer := startHostPeer(t, func(number int, socket *WebSocket, _ string) {
		for {
			if number == 1 {
				// The stalled owner reads its request and never answers it.
				if _, ok := readPacketUntilClose(t, socket); !ok {
					return
				}
				continue
			}
			packet, ok := readPacketUntilClose(t, socket)
			if !ok {
				return
			}
			requests <- packet
			if _, isRequest := packet["id"]; !isRequest {
				continue
			}
			if !answerRequest(t, socket, packet) {
				return
			}
		}
	})
	started := time.Now()
	if _, err := ConnectCodexHost(context.Background(), peer.url(), HostOptions{Timeout: 100 * time.Millisecond}); err == nil ||
		!strings.Contains(err.Error(), "timed out") {
		t.Fatalf("the stalled connect = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the stalled connect took %s, so initialization added its own full wait", elapsed)
	}
	host, err := ConnectCodexHost(context.Background(), peer.url(), HostOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("the recovered connect: %v", err)
	}
	defer host.Close()
	value, err := host.Rpc.Request("model/list", map[string]any{})
	if err != nil {
		t.Fatalf("model/list: %v", err)
	}
	if fields, ok := nativejson.Fields(value); !ok || len(fields) != 0 {
		t.Fatalf("model/list = %#v", value)
	}
	if count := peer.count(); count != 2 {
		t.Fatalf("the peer saw %d connections", count)
	}
	var seen []string
	for len(seen) < 3 {
		select {
		case packet := <-requests:
			seen = append(seen, method(t, packet))
		case <-time.After(10 * time.Second):
			t.Fatalf("the recovered connection sent only %v", seen)
		}
	}
	if seen[0] != "initialize" || seen[1] != "initialized" || seen[2] != "model/list" {
		t.Fatalf("the recovered connection sent %v", seen)
	}
}

func TestConnectCodexHostSendsTheCredentialAndClientInfo(t *testing.T) {
	type observation struct {
		authorization string
		initialize    map[string]any
		initialized   map[string]any
	}
	observed := make(chan observation, 1)
	peer := startHostPeer(t, func(_ int, socket *WebSocket, authorization string) {
		initialize, ok := readPacket(t, socket)
		if !ok || !answerRequest(t, socket, initialize) {
			return
		}
		initialized, ok := readPacket(t, socket)
		if !ok {
			return
		}
		observed <- observation{authorization: authorization, initialize: initialize, initialized: initialized}
	})
	token := strings.Repeat("ab", 32)
	host, err := ConnectCodexHost(context.Background(), peer.url(), HostOptions{Token: &token})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer host.Close()
	select {
	case seen := <-observed:
		if seen.authorization != "Bearer "+token {
			t.Errorf("authorization = %q", seen.authorization)
		}
		if method(t, seen.initialize) != "initialize" {
			t.Errorf("the first packet was %#v", seen.initialize)
		}
		params, ok := seen.initialize["params"].(map[string]any)
		if !ok {
			t.Fatalf("initialize params = %#v", seen.initialize["params"])
		}
		client, ok := params["clientInfo"].(map[string]any)
		if !ok {
			t.Fatalf("clientInfo = %#v", params["clientInfo"])
		}
		if client["name"] != "filo" || client["title"] != "Filo" || client["version"] != "0.2.0" {
			t.Errorf("clientInfo = %#v", client)
		}
		capabilities, ok := params["capabilities"].(map[string]any)
		if !ok || capabilities["experimentalApi"] != true {
			t.Errorf("capabilities = %#v", params["capabilities"])
		}
		if method(t, seen.initialized) != "initialized" {
			t.Errorf("the second packet was %#v", seen.initialized)
		}
		if _, hasIdentifier := seen.initialized["id"]; hasIdentifier {
			t.Errorf("a notification must not carry an id: %#v", seen.initialized)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the peer never observed the handshake")
	}
}

func TestCloseCodexHostDoesNotWaitForThePeer(t *testing.T) {
	released := make(chan struct{})
	peer := startHostPeer(t, func(_ int, socket *WebSocket, _ string) {
		initialize, ok := readPacket(t, socket)
		if !ok || !answerRequest(t, socket, initialize) {
			return
		}
		// The peer then stops reading and never acknowledges a close handshake.
		<-released
	})
	t.Cleanup(func() { close(released) })
	host, err := ConnectCodexHost(context.Background(), peer.url(), HostOptions{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	started := time.Now()
	host.Close()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("close waited %s for the peer", elapsed)
	}
	host.Close()
	if _, err := host.Rpc.Request("model/list", map[string]any{}); err == nil {
		t.Error("a closed host must not accept requests")
	}
}

func TestPeerDisconnectRetiresPendingRequests(t *testing.T) {
	peer := startHostPeer(t, func(_ int, socket *WebSocket, _ string) {
		initialize, ok := readPacket(t, socket)
		if !ok || !answerRequest(t, socket, initialize) {
			return
		}
		if _, ok := readPacket(t, socket); !ok {
			return
		}
		if _, ok := readPacket(t, socket); !ok {
			return
		}
		socket.Terminate()
	})
	host, err := ConnectCodexHost(context.Background(), peer.url(), HostOptions{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer host.Close()
	if _, err := host.Rpc.Request("thread/read", map[string]any{"threadId": "one"}); err == nil ||
		err.Error() != "Codex host disconnected" {
		t.Fatalf("the request after a peer disconnect = %v", err)
	}
}

func TestConnectCodexHostRejectsUnusableEndpoints(t *testing.T) {
	valid := strings.Repeat("ab", 32)
	cases := []struct {
		name    string
		url     string
		token   *string
		timeout time.Duration
		want    string
	}{
		{"plain http endpoint", "http://127.0.0.1:1/", nil, 0, "The shared Codex host must use a loopback WebSocket URL"},
		{"secure endpoint", "wss://127.0.0.1:1/", nil, 0, "The shared Codex host must use a loopback WebSocket URL"},
		{"remote host", "ws://example.com:1/", nil, 0, "The shared Codex host must use a loopback WebSocket URL"},
		{"query", "ws://127.0.0.1:1/?token=1", nil, 0, "The shared Codex host must use a loopback WebSocket URL"},
		{"fragment", "ws://127.0.0.1:1/#x", nil, 0, "The shared Codex host must use a loopback WebSocket URL"},
		{"embedded credentials", "ws://u:p@127.0.0.1:1/", nil, 0, "The shared Codex host must use a loopback WebSocket URL"},
		{"missing scheme", "127.0.0.1:1", nil, 0, "The shared Codex host must use a loopback WebSocket URL"},
		{"short token", "ws://127.0.0.1:1/", pointer("abc"), 0, "Invalid private executor credential"},
		{"non hex token", "ws://127.0.0.1:1/", pointer(strings.Repeat("z", 64)), 0, "Invalid private executor credential"},
		{"negative deadline", "ws://127.0.0.1:1/", nil, -time.Second, "Invalid connection deadline"},
		{"uppercase token reaches the transport", "ws://127.0.0.1:1/", pointer(strings.ToUpper(valid)), 0, "Codex connection failed"},
		{"loopback name reaches the transport", "ws://localhost:1/", nil, 0, "Codex connection failed"},
		{"ipv6 loopback reaches the transport", "ws://[::1]:1/", nil, 0, "Codex connection failed"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := ConnectCodexHost(context.Background(), test.url, HostOptions{Token: test.token, Timeout: test.timeout})
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func pointer(value string) *string { return &value }

func TestHostTrimEndMatchesJavaScript(t *testing.T) {
	cases := []struct{ input, want string }{
		{"{\"a\":1}\n", "{\"a\":1}"},
		{"{\"a\":1}\r\n", "{\"a\":1}"},
		{"value \t\n", "value"},
		{"\u00a0\u3000\n", ""},
		{"\ufeff", ""},
		{"a\u2028", "a"},
		{"a b", "a b"},
		{"", ""},
	}
	for _, test := range cases {
		if got := hostTrimEnd(test.input); got != test.want {
			t.Errorf("hostTrimEnd(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
