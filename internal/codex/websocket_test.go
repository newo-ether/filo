package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rawPeer is a one-shot TCP peer that speaks the wire format by hand, so both
// sides of this package are checked against RFC 6455 rather than only against
// each other.
type rawPeer struct {
	url  string
	done chan struct{}
}

func startRawPeer(t *testing.T, handle func(conn net.Conn, reader *bufio.Reader)) *rawPeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	peer := &rawPeer{url: "ws://" + listener.Addr().String() + "/native", done: make(chan struct{})}
	go func() {
		defer close(peer.done)
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		handle(conn, bufio.NewReader(conn))
	}()
	return peer
}

func (peer *rawPeer) wait(t *testing.T) {
	t.Helper()
	select {
	case <-peer.done:
	case <-time.After(20 * time.Second):
		t.Fatal("the raw peer did not finish")
	}
}

const (
	testWebSocketKey    = "dGhlIHNhbXBsZSBub25jZQ=="
	testWebSocketAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
)

func readWebSocketRequest(t *testing.T, reader *bufio.Reader) *http.Request {
	t.Helper()
	request, err := http.ReadRequest(reader)
	if err != nil {
		t.Errorf("read the handshake request: %v", err)
		return nil
	}
	return request
}

func writeWebSocketResponse(t *testing.T, conn net.Conn, accept string) {
	t.Helper()
	payload := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Errorf("write the handshake response: %v", err)
	}
}

func completeServerHandshake(t *testing.T, conn net.Conn, reader *bufio.Reader) *http.Request {
	t.Helper()
	request := readWebSocketRequest(t, reader)
	if request == nil {
		return nil
	}
	writeWebSocketResponse(t, conn, webSocketAccept(request.Header.Get("Sec-WebSocket-Key")))
	return request
}

type rawFrame struct {
	final    bool
	reserved bool
	opcode   byte
	masked   bool
	payload  []byte
}

func writeRawFrame(t *testing.T, conn net.Conn, frame rawFrame) {
	t.Helper()
	header := []byte{frame.opcode}
	if frame.final {
		header[0] |= 0x80
	}
	if frame.reserved {
		header[0] |= 0x40
	}
	size := len(frame.payload)
	switch {
	case size < 126:
		header = append(header, byte(size))
	case size <= 65535:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(size))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(size))
	}
	body := frame.payload
	if frame.masked {
		header[1] |= 0x80
		key := [4]byte{0x11, 0x22, 0x33, 0x44}
		header = append(header, key[:]...)
		body = make([]byte, size)
		for index := range frame.payload {
			body[index] = frame.payload[index] ^ key[index%4]
		}
	}
	if _, err := conn.Write(append(header, body...)); err != nil {
		t.Errorf("write a frame: %v", err)
	}
}

func readRawFrame(t *testing.T, reader *bufio.Reader) (rawFrame, error) {
	frame := rawFrame{}
	header := make([]byte, 2)
	if _, err := readFull(reader, header); err != nil {
		return frame, err
	}
	frame.final = header[0]&0x80 != 0
	frame.opcode = header[0] & 0x0f
	frame.masked = header[1]&0x80 != 0
	size := uint64(header[1] & 0x7f)
	switch size {
	case 126:
		extended := make([]byte, 2)
		if _, err := readFull(reader, extended); err != nil {
			return frame, err
		}
		size = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err := readFull(reader, extended); err != nil {
			return frame, err
		}
		size = binary.BigEndian.Uint64(extended)
	}
	var key [4]byte
	if frame.masked {
		if _, err := readFull(reader, key[:]); err != nil {
			return frame, err
		}
	}
	payload := make([]byte, size)
	if _, err := readFull(reader, payload); err != nil {
		return frame, err
	}
	if frame.masked {
		for index := range payload {
			payload[index] ^= key[index%4]
		}
	}
	frame.payload = payload
	return frame, nil
}

func readRawFrameOrFail(t *testing.T, reader *bufio.Reader) (rawFrame, bool) {
	t.Helper()
	frame, err := readRawFrame(t, reader)
	if err != nil {
		t.Errorf("read a frame: %v", err)
		return rawFrame{}, false
	}
	return frame, true
}

func readFull(reader *bufio.Reader, buffer []byte) (int, error) {
	total := 0
	for total < len(buffer) {
		count, err := reader.Read(buffer[total:])
		total += count
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func pattern(bytes int) []byte {
	payload := make([]byte, bytes)
	for index := range payload {
		payload[index] = byte('a' + index%26)
	}
	return payload
}

func TestWebSocketAcceptMatchesTheRfcExample(t *testing.T) {
	if accept := webSocketAccept(testWebSocketKey); accept != testWebSocketAccept {
		t.Fatalf("accept = %q, want %q", accept, testWebSocketAccept)
	}
	header := http.Header{"Connection": {"keep-alive, Upgrade"}}
	if !headerContains(header, "Connection", "upgrade") {
		t.Error("a token inside a comma separated header value must match")
	}
	if headerContains(header, "Connection", "close") {
		t.Error("an absent token must not match")
	}
}

func TestDialWebSocketCompletesTheHandshakeAndEchoesMessages(t *testing.T) {
	sizes := []int{5, 200, 70000}
	peer := startRawPeer(t, func(conn net.Conn, reader *bufio.Reader) {
		request := completeServerHandshake(t, conn, reader)
		if request == nil {
			return
		}
		if request.URL.Path != "/native" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if value := request.Header.Get("Authorization"); value != "Bearer capability" {
			t.Errorf("authorization = %q", value)
		}
		if value := request.Header.Get("Sec-WebSocket-Version"); value != "13" {
			t.Errorf("version = %q", value)
		}
		if value := request.Header.Get("Origin"); value != "" {
			t.Errorf("the client sent an Origin header: %q", value)
		}
		for _, size := range sizes {
			frame, ok := readRawFrameOrFail(t, reader)
			if !ok {
				return
			}
			if !frame.final || frame.opcode != opText || !frame.masked {
				t.Errorf("frame %d = %+v", size, frame)
				return
			}
			if len(frame.payload) != size {
				t.Errorf("frame %d carried %d bytes", size, len(frame.payload))
				return
			}
			writeRawFrame(t, conn, rawFrame{final: true, opcode: opText, payload: frame.payload})
		}
	})
	headers := http.Header{}
	headers.Set("Authorization", "Bearer capability")
	socket, err := DialWebSocket(context.Background(), peer.url, headers, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer socket.Terminate()
	if socket.LocalAddr() == nil {
		t.Error("the socket must report its bound address")
	}
	for _, size := range sizes {
		payload := pattern(size)
		if err := socket.WriteText(payload); err != nil {
			t.Fatalf("write %d bytes: %v", size, err)
		}
		message, err := socket.ReadMessage()
		if err != nil {
			t.Fatalf("read %d bytes: %v", size, err)
		}
		if !bytes.Equal(message, payload) {
			t.Fatalf("the %d byte echo did not survive the round trip", size)
		}
	}
	peer.wait(t)
}

func TestReadMessageAssemblesFragmentsAndAnswersPings(t *testing.T) {
	peer := startRawPeer(t, func(conn net.Conn, reader *bufio.Reader) {
		if completeServerHandshake(t, conn, reader) == nil {
			return
		}
		writeRawFrame(t, conn, rawFrame{opcode: opText, payload: []byte("frag")})
		writeRawFrame(t, conn, rawFrame{final: true, opcode: opPing, payload: []byte("ping")})
		writeRawFrame(t, conn, rawFrame{final: true, opcode: opContinuation, payload: []byte("mented")})
		frame, ok := readRawFrameOrFail(t, reader)
		if !ok {
			return
		}
		if frame.opcode != opPong || !frame.masked || string(frame.payload) != "ping" {
			t.Errorf("the client answered a ping with %+v", frame)
			return
		}
		writeRawFrame(t, conn, rawFrame{final: true, opcode: opClose, payload: []byte{0x03, 0xe8}})
		frame, ok = readRawFrameOrFail(t, reader)
		if !ok {
			return
		}
		if frame.opcode != opClose {
			t.Errorf("the client answered a close with opcode %d", frame.opcode)
		}
	})
	socket, err := DialWebSocket(context.Background(), peer.url, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer socket.Terminate()
	message, err := socket.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(message) != "fragmented" {
		t.Fatalf("message = %q", message)
	}
	if _, err := socket.ReadMessage(); !errors.Is(err, ErrWebSocketClosed) {
		t.Fatalf("the read after close = %v", err)
	}
	if err := socket.WriteText([]byte("after the close")); !errors.Is(err, ErrWebSocketClosed) {
		t.Fatalf("the write after close = %v", err)
	}
	peer.wait(t)
}

func TestDialWebSocketRejectsUnusableHandshakes(t *testing.T) {
	t.Run("wrong accept key", func(t *testing.T) {
		peer := startRawPeer(t, func(conn net.Conn, reader *bufio.Reader) {
			if readWebSocketRequest(t, reader) == nil {
				return
			}
			writeWebSocketResponse(t, conn, "AAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		})
		_, err := DialWebSocket(context.Background(), peer.url, nil, 5*time.Second)
		if err == nil || err.Error() != "Invalid WebSocket handshake" {
			t.Errorf("dial error = %v", err)
		}
		peer.wait(t)
	})
	t.Run("plain http response", func(t *testing.T) {
		peer := startRawPeer(t, func(conn net.Conn, reader *bufio.Reader) {
			if readWebSocketRequest(t, reader) == nil {
				return
			}
			if _, err := conn.Write([]byte("HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")); err != nil {
				t.Errorf("write the refusal: %v", err)
			}
		})
		_, err := DialWebSocket(context.Background(), peer.url, nil, 5*time.Second)
		if err == nil || err.Error() != "Invalid WebSocket handshake" {
			t.Errorf("dial error = %v", err)
		}
		peer.wait(t)
	})
	t.Run("unsupported scheme", func(t *testing.T) {
		if _, err := DialWebSocket(context.Background(), "http://127.0.0.1:1/native", nil, time.Second); err == nil {
			t.Error("a plain http endpoint must be rejected")
		}
	})
	t.Run("unreachable endpoint", func(t *testing.T) {
		if _, err := DialWebSocket(context.Background(), "ws://127.0.0.1:1/native", nil, 500*time.Millisecond); err == nil {
			t.Error("a refused connection must fail")
		}
	})
}

func TestReadMessageRejectsProtocolViolations(t *testing.T) {
	cases := []struct {
		name    string
		message string
		handle  func(conn net.Conn)
	}{
		{
			name:    "masked server frame",
			message: "Invalid WebSocket masking",
			handle: func(conn net.Conn) {
				writeRawFrame(t, conn, rawFrame{final: true, opcode: opText, masked: true, payload: []byte("hi")})
			},
		},
		{
			name:    "reserved bits",
			message: "Reserved WebSocket bits are set",
			handle: func(conn net.Conn) {
				writeRawFrame(t, conn, rawFrame{final: true, reserved: true, opcode: opText, payload: []byte("hi")})
			},
		},
		{
			name:    "oversized control frame",
			message: "Invalid WebSocket control frame",
			handle: func(conn net.Conn) {
				writeRawFrame(t, conn, rawFrame{final: true, opcode: opPing, payload: pattern(maxWebSocketControlBytes + 1)})
			},
		},
		{
			name:    "continuation without a start",
			message: "Unexpected WebSocket continuation",
			handle: func(conn net.Conn) {
				writeRawFrame(t, conn, rawFrame{final: true, opcode: opContinuation, payload: []byte("hi")})
			},
		},
		{
			name:    "interleaved messages",
			message: "Interleaved WebSocket message",
			handle: func(conn net.Conn) {
				writeRawFrame(t, conn, rawFrame{opcode: opText, payload: []byte("one")})
				writeRawFrame(t, conn, rawFrame{opcode: opText, payload: []byte("two")})
			},
		},
		{
			name:    "declared length beyond the bound",
			message: "WebSocket message exceeds its bound",
			handle: func(conn net.Conn) {
				header := []byte{0x81, 127, 0, 0, 0, 0, 0, 0, 0, 0}
				binary.BigEndian.PutUint64(header[2:], uint64(maxWebSocketMessageBytes)+1)
				if _, err := conn.Write(header); err != nil {
					t.Errorf("write the oversized header: %v", err)
				}
			},
		},
		{
			name:    "unknown opcode",
			message: "Unsupported WebSocket opcode",
			handle: func(conn net.Conn) {
				writeRawFrame(t, conn, rawFrame{final: true, opcode: 0x3, payload: []byte("hi")})
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			peer := startRawPeer(t, func(conn net.Conn, reader *bufio.Reader) {
				if completeServerHandshake(t, conn, reader) == nil {
					return
				}
				test.handle(conn)
			})
			socket, err := DialWebSocket(context.Background(), peer.url, nil, 5*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer socket.Terminate()
			if _, err := socket.ReadMessage(); err == nil || err.Error() != test.message {
				t.Fatalf("read error = %v, want %q", err, test.message)
			}
			peer.wait(t)
		})
	}
}

func TestTerminateEndsTheConnection(t *testing.T) {
	peer := startRawPeer(t, func(conn net.Conn, reader *bufio.Reader) {
		if completeServerHandshake(t, conn, reader) == nil {
			return
		}
		_, _ = readRawFrame(t, reader)
	})
	socket, err := DialWebSocket(context.Background(), peer.url, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	socket.Terminate()
	socket.Terminate()
	if err := socket.WriteText([]byte("late")); !errors.Is(err, ErrWebSocketClosed) {
		t.Errorf("the write after terminate = %v", err)
	}
	if _, err := socket.ReadMessage(); !errors.Is(err, ErrWebSocketClosed) {
		t.Errorf("the read after terminate = %v", err)
	}
	peer.wait(t)
}

func TestUpgradeWebSocketServesTheNativeOwner(t *testing.T) {
	received := make(chan string, 1)
	failures := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		socket, err := UpgradeWebSocket(writer, request)
		if err != nil {
			failures <- err
			return
		}
		defer socket.Terminate()
		if err := socket.WriteText([]byte("from the native owner")); err != nil {
			failures <- err
			return
		}
		message, err := socket.ReadMessage()
		if err != nil {
			failures <- err
			return
		}
		received <- string(message)
	}))
	defer server.Close()
	socket, err := DialWebSocket(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/native", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer socket.Terminate()
	message, err := socket.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(message) != "from the native owner" {
		t.Fatalf("message = %q", message)
	}
	if err := socket.WriteText([]byte("from filo")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case value := <-received:
		if value != "from filo" {
			t.Errorf("the server read %q", value)
		}
	case err := <-failures:
		t.Fatalf("the server failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the server never read the client message")
	}
}

func TestUpgradeWebSocketRejectsUnusableRequests(t *testing.T) {
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		socket, err := UpgradeWebSocket(writer, request)
		if err != nil {
			return
		}
		socket.Terminate()
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	plain, err := http.Get(server.URL + "/native")
	if err != nil {
		t.Fatalf("plain request: %v", err)
	}
	defer plain.Body.Close()
	if plain.StatusCode != http.StatusBadRequest {
		t.Errorf("plain request status = %d", plain.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/native", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Key", testWebSocketKey)
	request.Header.Set("Sec-WebSocket-Version", "12")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("old version request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("old version status = %d", response.StatusCode)
	}

	recorder := httptest.NewRecorder()
	local := httptest.NewRequest(http.MethodGet, "/native", nil)
	local.Header.Set("Upgrade", "websocket")
	local.Header.Set("Connection", "Upgrade")
	local.Header.Set("Sec-WebSocket-Key", testWebSocketKey)
	local.Header.Set("Sec-WebSocket-Version", "13")
	if _, err := UpgradeWebSocket(recorder, local); err == nil || err.Error() != "WebSocket upgrade is unavailable" {
		t.Errorf("non hijackable writer error = %v", err)
	}
	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("non hijackable writer status = %d", recorder.Code)
	}
}

func TestUpgradeWebSocketRejectsUnmaskedClientFrames(t *testing.T) {
	results := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		socket, err := UpgradeWebSocket(writer, request)
		if err != nil {
			results <- err
			return
		}
		defer socket.Terminate()
		_, err = socket.ReadMessage()
		results <- err
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	handshake := "GET /native HTTP/1.1\r\nHost: " + strings.TrimPrefix(server.URL, "http://") + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + testWebSocketKey + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(handshake)); err != nil {
		t.Fatalf("write the handshake: %v", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read the handshake response: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d", response.StatusCode)
	}
	if response.Header.Get("Sec-WebSocket-Accept") != testWebSocketAccept {
		t.Fatalf("accept = %q", response.Header.Get("Sec-WebSocket-Accept"))
	}
	writeRawFrame(t, conn, rawFrame{final: true, opcode: opText, payload: []byte("unmasked")})
	select {
	case err := <-results:
		if err == nil || err.Error() != "Invalid WebSocket masking" {
			t.Fatalf("server read error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server never rejected the unmasked frame")
	}
}
