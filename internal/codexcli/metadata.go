package codexcli

import (
	"context"
	"errors"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

// Metadata connections are short-lived. Each one verifies both kernel peer and
// native store identity; reconnect never replays a native mutation.
func (a *Adapter) metadata(ctx context.Context) (*codex.NativeMetadata, func(), error) {
	scope, err := a.scope(ctx)
	if err != nil {
		return nil, nil, err
	}
	if a.options.SocketPath == "" || a.options.VerifyDaemon == nil {
		return nil, nil, errors.New("Local Codex daemon is unavailable")
	}
	a.mu.Lock()
	if a.closed || a.metadataReads >= 2 {
		a.mu.Unlock()
		return nil, nil, errors.New("Codex CLI metadata read limit")
	}
	a.metadataReads++
	a.daemonState = "connecting"
	a.mu.Unlock()
	admitted := false
	defer func() {
		if !admitted {
			a.mu.Lock()
			a.metadataReads--
			a.mu.Unlock()
		}
	}()
	host, err := codex.ConnectLocalDaemon(ctx, codex.LocalDaemonOptions{
		SocketPath: a.options.SocketPath, UserID: a.options.UserID, Timeout: a.options.Timeout,
		VerifyIdentity: func(ctx context.Context, identity codex.LocalDaemonIdentity) error {
			if err := a.options.VerifyDaemon(ctx, identity); err != nil {
				return err
			}
			return a.sameScope(ctx, scope)
		},
	})
	if err != nil {
		a.mu.Lock()
		a.daemonState = "unavailable"
		a.mu.Unlock()
		return nil, nil, err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		host.Close()
		return nil, nil, ErrUnavailable
	}
	a.connections[host] = struct{}{}
	a.daemonState = "connected"
	a.mu.Unlock()
	stop := context.AfterFunc(ctx, host.Close)
	release := func() {
		stop()
		host.Close()
		a.mu.Lock()
		delete(a.connections, host)
		a.metadataReads--
		a.mu.Unlock()
	}
	admitted = true
	return codex.NewNativeMetadata(host.Rpc), release, nil
}

func (a *Adapter) Models(ctx context.Context) ([]protocol.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, a.readTimeout())
	defer cancel()
	scope, err := a.scope(ctx)
	if err != nil {
		return nil, err
	}
	metadata, release, err := a.metadata(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	models, err := metadata.Models()
	if cause := ctx.Err(); cause != nil {
		return nil, cause
	}
	if err != nil {
		return nil, err
	}
	if err := a.sameScope(ctx, scope); err != nil {
		return nil, err
	}
	return models, nil
}

func (a *Adapter) Usage(ctx context.Context) (protocol.AccountUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.readTimeout())
	defer cancel()
	scope, err := a.scope(ctx)
	if err != nil {
		return protocol.AccountUsage{}, err
	}
	metadata, release, err := a.metadata(ctx)
	if err != nil {
		return protocol.AccountUsage{}, err
	}
	defer release()
	usage, err := metadata.Usage()
	if cause := ctx.Err(); cause != nil {
		return protocol.AccountUsage{}, cause
	}
	if err != nil {
		return protocol.AccountUsage{}, err
	}
	if err := a.sameScope(ctx, scope); err != nil {
		return protocol.AccountUsage{}, err
	}
	return usage, nil
}

func (a *Adapter) readTimeout() time.Duration {
	if a.options.Timeout > 0 {
		return a.options.Timeout
	}
	return 15 * time.Second
}

// Every execution operation is refused before a scope check or native request.
// Browser admission cannot call thread/resume, acquire a writer, or start work.
func (*Adapter) ValidateAccess(context.Context, string, string) error { return ErrUnsupported }
func (*Adapter) Attach(context.Context, string) (func(), error)       { return nil, ErrUnsupported }
func (*Adapter) Create(context.Context, string, string, protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	return protocol.Session{}, protocol.SendReceipt{}, ErrUnsupported
}
func (*Adapter) Send(context.Context, string, string, string) (protocol.SendReceipt, error) {
	return protocol.SendReceipt{}, ErrUnsupported
}
func (*Adapter) SetModel(context.Context, string, string) error { return ErrUnsupported }
func (*Adapter) Stop(context.Context, string, string) error     { return ErrUnsupported }
func (*Adapter) UpdateSettings(context.Context, string, protocol.SessionSettings) error {
	return ErrUnsupported
}
func (*Adapter) Rename(context.Context, string, string) (protocol.Session, error) {
	return protocol.Session{}, ErrUnsupported
}
func (*Adapter) Archive(context.Context, string) (string, error) { return "", ErrUnsupported }
