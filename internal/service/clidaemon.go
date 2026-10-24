package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// Existing native control sockets are the only production shared-CLI endpoint.
// Missing sockets are offline; discovery never initializes or launches Codex.
func existingCLIDaemon(socket, userID string, home func(context.Context) (string, error), verify func(context.Context, codex.LocalDaemonIdentity) error) func(context.Context) (*codex.CodexHost, error) {
	return func(ctx context.Context) (*codex.CodexHost, error) {
		selected, err := home(ctx)
		if err != nil {
			return nil, err
		}
		path := socket
		if path == "" {
			path = filepath.Join(selected, "app-server-control", "app-server-control.sock")
		}
		if _, err := os.Lstat(path); err != nil {
			return nil, codexcliUnavailable()
		}
		return codex.ConnectLocalDaemon(ctx, codex.LocalDaemonOptions{SocketPath: path, UserID: userID,
			Timeout: 2 * time.Second, VerifyIdentity: verify})
	}
}

func (f *cliSharedFactory) daemonCatalog(ctx context.Context) ([]protocol.Session, error) {
	host, err := f.daemon(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []protocol.Session{}, nil
	}
	defer host.Close()
	ids, err := cliSharedLoaded(host)
	if err != nil {
		return nil, err
	}
	rows := []protocol.Session{}
	tasks := map[string]cliSharedRecord{}
	for _, id := range ids {
		if err := protocol.ValidateScope(ctx); err != nil {
			return nil, err
		}
		value, err := host.Rpc.RequestTimeout("thread/read", map[string]any{"threadId": id, "includeTurns": false}, 2*time.Second)
		if err != nil {
			continue
		}
		fields, _ := nativejson.Fields(value)
		thread, _ := nativejson.Fields(fields["thread"])
		if nativejson.Text(thread["id"]) != id || thread["parentThreadId"] != nil {
			continue
		}
		row, err := cliLiveSession(value)
		if err != nil {
			continue
		}
		tasks[id] = cliSharedRecord{}
		rows = append(rows, row)
	}
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.tasks = tasks
	f.mu.Unlock()
	return rows, nil
}

func (f *cliSharedFactory) followDaemon(ctx context.Context, id string) (TaskSessionsConnection, error) {
	owned, _ := f.Owns(id)
	if !owned {
		return nil, codexcliUnavailable()
	}
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, err
	}
	host, err := f.daemon(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := cliSharedLoaded(host)
	if scopeErr := protocol.ValidateScope(ctx); scopeErr != nil {
		host.Close()
		return nil, scopeErr
	}
	if err != nil || !containsTurn(ids, id) {
		host.Close()
		return nil, errors.New("Native CLI thread is no longer loaded; it was not resumed")
	}
	return &cliDaemonConnection{host: host, state: &codex.NativeLiveState{Rpc: host.Rpc, ID: id}}, nil
}

type cliDaemonConnection struct {
	host  *codex.CodexHost
	state *codex.NativeLiveState
}

func (c *cliDaemonConnection) Transport() *codex.Rpc             { return c.host.Rpc }
func (c *cliDaemonConnection) Close()                            { c.host.Close() }
func (c *cliDaemonConnection) Observe(method string, params any) { c.state.Receive(method, params) }
func (c *cliDaemonConnection) TaskState(id string) (any, error)  { return c.state.State(id) }
