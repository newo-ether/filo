package codex

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

// LocalDaemonOptions names an existing selected-user daemon. This connector
// never discovers, starts, restarts, resumes, or terminates a native process.
type LocalDaemonOptions struct {
	SocketPath string
	// UserID is the selected Windows SID or decimal Unix uid, not a username.
	UserID  string
	Timeout time.Duration
	// VerifyIdentity must bind the authenticated peer's reported home to the
	// configured native store/account. A matching path alone is not that proof.
	VerifyIdentity func(context.Context, LocalDaemonIdentity) error
}

// LocalDaemonIdentity is private verification input, never a public descriptor.
// CodexHome comes from initialize; UserID comes from kernel peer verification.
type LocalDaemonIdentity struct {
	CodexHome      string
	UserID         string
	PlatformFamily string
	PlatformOS     string
	UserAgent      string
}

// ConnectLocalDaemon authenticates the local socket before sending the HTTP
// upgrade, then verifies initialize before admitting any native operation.
func ConnectLocalDaemon(ctx context.Context, options LocalDaemonOptions) (_ *CodexHost, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(options.SocketPath) || strings.ContainsAny(options.SocketPath, "\x00\r\n") ||
		options.UserID == "" || options.Timeout < 0 || options.VerifyIdentity == nil {
		return nil, errors.New("Invalid local Codex daemon configuration")
	}
	budget := options.Timeout
	if budget == 0 {
		budget = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	defer func() {
		if resultErr != nil && ctx.Err() != nil {
			resultErr = ctx.Err()
		}
	}()
	path, release, err := validateDaemonSocket(options.SocketPath, options.UserID)
	if err != nil {
		return nil, err
	}
	defer release()
	if len([]byte(path))+1 > 108 {
		return nil, errors.New("Canonical Codex daemon socket address exceeds AF_UNIX limit")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, errors.New("Local Codex daemon is unavailable")
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = connection.Close()
		}
	}()
	if err := verifyDaemonPeer(connection, options.UserID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = connection.SetDeadline(deadline)
	// The native control socket runs RFC6455 over UDS, not raw JSONL.
	socket, err := handshakeWebSocket(ctx, connection, &url.URL{Scheme: "ws", Host: "localhost", Path: "/"}, http.Header{})
	if err != nil {
		return nil, errors.New("Local Codex daemon handshake failed")
	}
	_ = connection.SetDeadline(deadline)
	socket.LimitMessage(4 << 20)
	host := newCodexSocketHost(socket)
	defer func() {
		if !accepted {
			host.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	value, err := host.Rpc.RequestTimeout("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "filo", "title": "Filo", "version": codexHostClientVersion},
		"capabilities": map[string]any{"experimentalApi": true},
	}, codexHostRemaining(deadline, codexHostDefaultInitializeTimeout))
	if err != nil {
		return nil, err
	}
	fields, ok := nativejson.Fields(value)
	identity := LocalDaemonIdentity{UserID: options.UserID, CodexHome: nativejson.Text(fields["codexHome"]),
		PlatformFamily: nativejson.Text(fields["platformFamily"]), PlatformOS: nativejson.Text(fields["platformOs"]),
		UserAgent: nativejson.Text(fields["userAgent"])}
	if !ok || !filepath.IsAbs(identity.CodexHome) {
		return nil, errors.New("Local Codex daemon did not report its native store")
	}
	if err := options.VerifyIdentity(ctx, identity); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := host.Rpc.Notify("initialized", nil); err != nil {
		return nil, err
	}
	if !stop() {
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	accepted = true
	return host, nil
}
