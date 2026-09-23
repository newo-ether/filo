package codex

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// maxWebSocketMessageBytes bounds one assembled message. The native app
	// server streams bounded JSONL packets, so this is a hard stop far above
	// any legal packet rather than a tuning knob.
	maxWebSocketMessageBytes = 100 << 20
	// maxWebSocketControlBytes is the RFC 6455 control-frame ceiling.
	maxWebSocketControlBytes = 125
	webSocketGuid            = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

const (
	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xa
)

// ErrWebSocketClosed reports a peer close or a terminated transport.
var ErrWebSocketClosed = errors.New("WebSocket closed")

// WebSocket is one RFC 6455 connection carrying whole messages. The native
// owner speaks one text JSONL packet per message, so every write is a single
// final frame and reads reassemble fragments. Client frames are masked, server
// frames are not, exactly as the protocol requires.
type WebSocket struct {
	conn   net.Conn
	reader *bufio.Reader
	client bool

	writeMu sync.Mutex
	closed  bool
	limit   int
}

// DialWebSocket opens a client connection. The handshake is bounded by timeout
// when it is positive, and the returned socket carries only the caller's own
// TCP connection: terminating it never stops the peer.
func DialWebSocket(ctx context.Context, rawURL string, headers http.Header, timeout time.Duration) (*WebSocket, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("Invalid WebSocket URL")
	}
	if !strings.EqualFold(endpoint.Scheme, "ws") && !strings.EqualFold(endpoint.Scheme, "wss") {
		return nil, errors.New("Invalid WebSocket URL")
	}
	host := endpoint.Host
	if endpoint.Port() == "" {
		host = net.JoinHostPort(endpoint.Hostname(), "80")
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	key, err := webSocketKey()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	path := endpoint.EscapedPath()
	if path == "" {
		path = "/"
	}
	if endpoint.RawQuery != "" {
		path += "?" + endpoint.RawQuery
	}
	var handshake strings.Builder
	handshake.WriteString("GET " + path + " HTTP/1.1\r\n")
	handshake.WriteString("Host: " + endpoint.Host + "\r\n")
	handshake.WriteString("Upgrade: websocket\r\n")
	handshake.WriteString("Connection: Upgrade\r\n")
	handshake.WriteString("Sec-WebSocket-Key: " + key + "\r\n")
	handshake.WriteString("Sec-WebSocket-Version: 13\r\n")
	for name, values := range headers {
		for _, value := range values {
			handshake.WriteString(name + ": " + value + "\r\n")
		}
	}
	handshake.WriteString("\r\n")
	if _, err := io.WriteString(conn, handshake.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	request := &http.Request{Method: http.MethodGet}
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols ||
		!headerContains(response.Header, "Upgrade", "websocket") ||
		!headerContains(response.Header, "Connection", "upgrade") ||
		response.Header.Get("Sec-WebSocket-Accept") != webSocketAccept(key) {
		_ = conn.Close()
		return nil, errors.New("Invalid WebSocket handshake")
	}
	_ = conn.SetDeadline(time.Time{})
	return &WebSocket{conn: conn, reader: reader, client: true, limit: maxWebSocketMessageBytes}, nil
}

// UpgradeWebSocket completes the server side of a handshake on a hijacked
// connection. The caller owns the returned socket.
func UpgradeWebSocket(writer http.ResponseWriter, request *http.Request) (*WebSocket, error) {
	key := request.Header.Get("Sec-WebSocket-Key")
	if request.Method != http.MethodGet || !headerContains(request.Header, "Upgrade", "websocket") ||
		!headerContains(request.Header, "Connection", "upgrade") || key == "" ||
		request.Header.Get("Sec-WebSocket-Version") != "13" {
		writer.WriteHeader(http.StatusBadRequest)
		return nil, errors.New("Invalid WebSocket handshake")
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		writer.WriteHeader(http.StatusNotImplemented)
		return nil, errors.New("WebSocket upgrade is unavailable")
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	response := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + webSocketAccept(key) + "\r\n\r\n"
	if _, err := buffered.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := buffered.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &WebSocket{conn: conn, reader: buffered.Reader, limit: maxWebSocketMessageBytes}, nil
}

// LimitMessage narrows this socket's assembled message bound, which a private
// executor sets to its own payload ceiling. It only ever tightens the default
// and must be applied before the first read.
func (ws *WebSocket) LimitMessage(bytes int) {
	if bytes > 0 && bytes < ws.messageLimit() {
		ws.limit = bytes
	}
}

func (ws *WebSocket) messageLimit() int {
	if ws.limit <= 0 {
		return maxWebSocketMessageBytes
	}
	return ws.limit
}

// Closed reports whether this socket was terminated locally, the Go equivalent
// of a peer socket that is no longer OPEN.
func (ws *WebSocket) Closed() bool {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()
	return ws.closed
}

// SetWriteDeadline bounds a single write so a stalled peer cannot stall its
// caller. A zero time clears the deadline.
func (ws *WebSocket) SetWriteDeadline(deadline time.Time) error {
	return ws.conn.SetWriteDeadline(deadline)
}

// CloseWith sends one close frame carrying a status code and reason, then drops
// the connection without waiting for the peer's acknowledgement.
func (ws *WebSocket) CloseWith(code uint16, reason string) {
	if len(reason) > maxWebSocketControlBytes-2 {
		reason = reason[:maxWebSocketControlBytes-2]
		for len(reason) > 0 && reason[len(reason)-1]&0xc0 == 0x80 {
			reason = reason[:len(reason)-1]
		}
	}
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, code)
	payload = append(payload, reason...)
	_ = ws.writeFrame(opClose, payload)
	ws.Terminate()
}

// ReadMessage returns the next whole text or binary message. A peer close ends
// the connection and reports ErrWebSocketClosed.
func (ws *WebSocket) ReadMessage() ([]byte, error) {
	var assembled []byte
	started := false
	for {
		final, opcode, payload, err := ws.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opPing:
			if err := ws.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = ws.writeFrame(opClose, payload)
			ws.Terminate()
			return nil, ErrWebSocketClosed
		case opContinuation:
			if !started {
				return nil, errors.New("Unexpected WebSocket continuation")
			}
		case opText, opBinary:
			if started {
				return nil, errors.New("Interleaved WebSocket message")
			}
			started = true
		default:
			return nil, errors.New("Unsupported WebSocket opcode")
		}
		assembled = append(assembled, payload...)
		if len(assembled) > ws.messageLimit() {
			return nil, errors.New("WebSocket message exceeds its bound")
		}
		if final {
			return assembled, nil
		}
	}
}

// WriteText sends one final text frame.
func (ws *WebSocket) WriteText(payload []byte) error {
	return ws.writeFrame(opText, payload)
}

// Terminate drops this connection without a close handshake, matching the ws
// terminate call the native owner relies on to avoid waiting on a peer.
func (ws *WebSocket) Terminate() {
	ws.writeMu.Lock()
	ws.closed = true
	ws.writeMu.Unlock()
	_ = ws.conn.Close()
}

// LocalAddr reports the bound address, which the executor publishes as its
// loopback endpoint.
func (ws *WebSocket) LocalAddr() net.Addr { return ws.conn.LocalAddr() }

func (ws *WebSocket) readFrame() (bool, byte, []byte, error) {
	return ws.readFrameBounded(ws.messageLimit(), false)
}

func (ws *WebSocket) readFrameBounded(retain int, discard bool) (bool, byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(ws.reader, header); err != nil {
		return false, 0, nil, webSocketTransportError(err)
	}
	final := header[0]&0x80 != 0
	if header[0]&0x70 != 0 {
		return false, 0, nil, errors.New("Reserved WebSocket bits are set")
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	if ws.client == masked {
		// A server must mask, a client must not.
		return false, 0, nil, errors.New("Invalid WebSocket masking")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err := io.ReadFull(ws.reader, extended); err != nil {
			return false, 0, nil, webSocketTransportError(err)
		}
		length = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err := io.ReadFull(ws.reader, extended); err != nil {
			return false, 0, nil, webSocketTransportError(err)
		}
		length = binary.BigEndian.Uint64(extended)
	}
	if opcode >= opClose && (!final || length > maxWebSocketControlBytes) {
		return false, 0, nil, errors.New("Invalid WebSocket control frame")
	}
	if length > 1<<63-1 || (!discard && length > uint64(ws.messageLimit())) {
		return false, 0, nil, errors.New("WebSocket message exceeds its bound")
	}
	var key [4]byte
	if masked {
		if _, err := io.ReadFull(ws.reader, key[:]); err != nil {
			return false, 0, nil, webSocketTransportError(err)
		}
	}
	if discard && opcode < opClose && length > uint64(retain) {
		if _, err := io.CopyN(io.Discard, ws.reader, int64(length)); err != nil {
			return false, 0, nil, webSocketTransportError(err)
		}
		return final, opcode, nil, errDiscardedFrame
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(ws.reader, payload); err != nil {
		return false, 0, nil, webSocketTransportError(err)
	}
	if masked {
		for index := range payload {
			payload[index] ^= key[index%4]
		}
	}
	return final, opcode, payload, nil
}

func (ws *WebSocket) writeFrame(opcode byte, payload []byte) error {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()
	if ws.closed {
		return ErrWebSocketClosed
	}
	header := make([]byte, 0, 14)
	header = append(header, 0x80|opcode)
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	body := payload
	if ws.client {
		header[1] |= 0x80
		// The masking key is four bytes, unlike the sixteen byte handshake
		// nonce, so it never shares the helper that builds Sec-WebSocket-Key.
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		header = append(header, key[:]...)
		masked := make([]byte, len(payload))
		for index := range payload {
			masked[index] = payload[index] ^ key[index%4]
		}
		body = masked
	}
	if _, err := ws.conn.Write(header); err != nil {
		return webSocketTransportError(err)
	}
	if len(body) > 0 {
		if _, err := ws.conn.Write(body); err != nil {
			return webSocketTransportError(err)
		}
	}
	return nil
}

// webSocketTransportError reports a dropped connection as the closed sentinel
// so callers never mistake a terminated peer for a protocol violation.
func webSocketTransportError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return ErrWebSocketClosed
	}
	var netError net.Error
	if errors.As(err, &netError) {
		return ErrWebSocketClosed
	}
	return err
}

func headerContains(header http.Header, name, value string) bool {
	for _, entry := range header.Values(name) {
		for _, token := range strings.Split(entry, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return true
			}
		}
	}
	return false
}

func webSocketKey() (string, error) {
	key, err := webSocketKeyBytes()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

func webSocketKeyBytes() ([16]byte, error) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, err
	}
	return key, nil
}

func webSocketAccept(key string) string {
	digest := sha1.Sum([]byte(key + webSocketGuid))
	return base64.StdEncoding.EncodeToString(digest[:])
}
