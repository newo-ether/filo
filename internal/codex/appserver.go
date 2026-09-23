package codex

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// codexHostClientVersion is the clientInfo version Filo reports to the shared
// native owner.
const codexHostClientVersion = "0.2.0"

// codexHostDefaultOpenTimeout bounds the transport handshake, and
// codexHostDefaultInitializeTimeout bounds the initialize request whenever the
// caller supplied no overall deadline.
const (
	codexHostDefaultOpenTimeout       = 10 * time.Second
	codexHostDefaultInitializeTimeout = 30 * time.Second
)

// codexHostMinimumTimeout keeps a caller supplied sub-millisecond budget from
// turning into Go's "no timeout at all" for a dialer.
const codexHostMinimumTimeout = time.Millisecond

var privateTokenPattern = regexp.MustCompile(`(?i)^[a-f0-9]{64}$`)

// HostOptions carries the optional private credential and the overall connect
// budget for ConnectCodexHost.
//
// Divergence from the TypeScript caller: timeoutMs === 0 was rejected as an
// invalid deadline there, while a Go Duration has no way to distinguish "0"
// from an absent value. Timeout == 0 therefore means "no deadline", and only a
// negative duration is rejected.
type HostOptions struct {
	// Token is the private executor credential. Nil means the host expects no
	// authentication header.
	Token *string
	// Timeout bounds the whole connect sequence. Zero means no deadline.
	Timeout time.Duration
}

// CodexHost is one client connection to the shared native owner. Closing it
// ends only this client's socket and never stops the host process.
type CodexHost struct {
	// Rpc is the JSONL transport the caller drives.
	Rpc *Rpc

	inputReader *io.PipeReader
	inputWriter *io.PipeWriter
	socket      *WebSocket

	closeOnce sync.Once
}

// ConnectCodexHost connects to the shared native owner and completes the
// initialize handshake. Every failure path closes the socket it opened, so a
// rejected connect never leaves a half-open client behind.
func ConnectCodexHost(ctx context.Context, rawURL string, options HostOptions) (*CodexHost, error) {
	if err := validateCodexHostURL(rawURL); err != nil {
		return nil, err
	}
	if options.Token != nil && !privateTokenPattern.MatchString(*options.Token) {
		return nil, errors.New("Invalid private executor credential")
	}
	if options.Timeout < 0 {
		return nil, errors.New("Invalid connection deadline")
	}
	var deadline time.Time
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	headers := http.Header{}
	if options.Token != nil {
		headers.Set("Authorization", "Bearer "+*options.Token)
	}
	socket, err := DialWebSocket(ctx, rawURL, headers, codexHostRemaining(deadline, codexHostDefaultOpenTimeout))
	if err != nil {
		return nil, codexConnectFailure(err)
	}
	host := &CodexHost{socket: socket}
	host.inputReader, host.inputWriter = io.Pipe()
	host.Rpc = NewRpc(host.inputReader, &hostWriter{socket: socket})
	go host.pumpInput()
	params := map[string]any{
		"clientInfo": map[string]any{
			"name": "filo", "title": "Filo", "version": codexHostClientVersion,
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}
	if _, err := host.Rpc.RequestTimeout("initialize", params, codexHostRemaining(deadline, codexHostDefaultInitializeTimeout)); err != nil {
		host.Close()
		return nil, err
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		host.Close()
		return nil, errors.New("Codex connection timed out")
	}
	if err := host.Rpc.Notify("initialized", nil); err != nil {
		host.Close()
		return nil, err
	}
	return host, nil
}

// Close ends this client's transport. It never waits for a peer close handshake,
// and it is safe to call repeatedly and alongside a peer disconnect.
func (h *CodexHost) Close() {
	h.closeOnce.Do(func() {
		h.Rpc.Close()
		h.socket.Terminate()
		_ = h.inputWriter.Close()
		_ = h.inputReader.Close()
	})
}

// pumpInput feeds every inbound WebSocket message into the RPC transport as one
// JSONL line, and retires the transport when the socket ends. The first cause
// wins, so a peer disconnect never degrades into a generic transport error.
func (h *CodexHost) pumpInput() {
	defer h.inputWriter.Close()
	for {
		message, err := h.socket.ReadMessage()
		if err != nil {
			h.Rpc.CloseWithError(codexHostCause(err))
			return
		}
		line := make([]byte, 0, len(message)+1)
		line = append(line, message...)
		line = append(line, '\n')
		if _, err := h.inputWriter.Write(line); err != nil {
			h.Rpc.CloseWithError(codexHostCause(err))
			return
		}
	}
}

// codexHostCause maps a transport failure onto the cause Filo reports to its
// callers, matching the message the TypeScript listeners produced.
func codexHostCause(err error) error {
	if errors.Is(err, ErrWebSocketClosed) {
		return errors.New("Codex host disconnected")
	}
	return errors.New("Codex WebSocket failed")
}

// hostWriter sends each serialized JSONL line as one text message with the
// trailing newline trimmed, exactly as the native owner expects.
type hostWriter struct {
	socket *WebSocket
}

func (w *hostWriter) Write(chunk []byte) (int, error) {
	if err := w.socket.WriteText([]byte(hostTrimEnd(string(chunk)))); err != nil {
		return 0, err
	}
	return len(chunk), nil
}

// hostTrimEnd mirrors String.prototype.trimEnd: JavaScript whitespace plus the
// byte order mark, which unicode.IsSpace does not cover.
func hostTrimEnd(value string) string {
	return strings.TrimRightFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\ufeff'
	})
}

func validateCodexHostURL(rawURL string) error {
	invalid := errors.New("The shared Codex host must use a loopback WebSocket URL")
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return invalid
	}
	if !strings.EqualFold(endpoint.Scheme, "ws") || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return invalid
	}
	switch strings.ToLower(endpoint.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
		return nil
	default:
		return invalid
	}
}

// codexHostRemaining rescales one step of the connect sequence so a supplied
// budget is never exceeded and never becomes a zero wait.
func codexHostRemaining(deadline time.Time, fallback time.Duration) time.Duration {
	remaining := fallback
	if !deadline.IsZero() {
		if left := time.Until(deadline); left < remaining {
			remaining = left
		}
	}
	if remaining < codexHostMinimumTimeout {
		return codexHostMinimumTimeout
	}
	return remaining
}

// codexConnectFailure separates a stalled peer from a refused one.
func codexConnectFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Codex connection timed out")
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return errors.New("Codex connection timed out")
	}
	return errors.New("Codex connection failed")
}
