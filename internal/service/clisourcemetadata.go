package service

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// CLIReadMetadata opens disposable, task-free native connections for read-only
// source metadata. It never shares a writer or owns an accepted native turn.
type CLIReadMetadata interface {
	Models(context.Context) ([]protocol.Model, error)
	Usage(context.Context) (protocol.AccountUsage, error)
	Close()
}

type cliMetadataConnection struct {
	rpc     *codex.Rpc
	release func()
}

type cliSourceMetadata struct {
	mu     sync.Mutex
	closed bool
	next   uint64
	active map[uint64]context.CancelFunc
	scope  func(context.Context) (string, error)
	verify func(context.Context, string) error
	open   func(context.Context) (cliMetadataConnection, error)
}

func newCLISourceMetadata(scope func(context.Context) (string, error), verify func(context.Context, string) error) *cliSourceMetadata {
	return &cliSourceMetadata{scope: scope, verify: verify, open: openCLIMetadata, active: map[uint64]context.CancelFunc{}}
}

func openCLIMetadata(ctx context.Context) (cliMetadataConnection, error) {
	if err := ctx.Err(); err != nil {
		return cliMetadataConnection{}, err
	}
	executable, err := resolveCLIRuntime()
	if err != nil {
		return cliMetadataConnection{}, err
	}
	command := exec.Command(executable, "app-server", "--listen", "stdio://")
	command.Dir = processWorkingDirectory()
	command.Env = nativeProcessEnvironment(executable)
	command.SysProcAttr = detachedProcessAttributes()
	input, err := command.StdinPipe()
	if err != nil {
		return cliMetadataConnection{}, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return cliMetadataConnection{}, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		input.Close()
		output.Close()
		return cliMetadataConnection{}, err
	}
	rpc := codex.NewRpc(output, input)
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	var once sync.Once
	release := func() {
		once.Do(func() {
			rpc.Close()
			// Only this task-free child can be released here. No task operation
			// is admitted by the metadata transport below.
			select {
			case <-done:
			case <-time.After(time.Second):
				_ = command.Process.Kill()
				<-done
			}
		})
	}
	return cliMetadataConnection{rpc: rpc, release: release}, nil
}

// cliMetadataRPC is the complete admitted post-handshake operation set.
type cliMetadataRPC struct {
	ctx context.Context
	rpc *codex.Rpc
}

func (r cliMetadataRPC) Request(method string, params any) (any, error) {
	if method != "model/list" && method != "account/rateLimits/read" {
		return nil, errors.New("CLI metadata cannot dispatch task operations")
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	deadline, _ := r.ctx.Deadline()
	return r.rpc.RequestTimeout(method, params, time.Until(deadline))
}

func (m *cliSourceMetadata) read(ctx context.Context, apply func(*codex.NativeMetadata) error) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m.mu.Lock()
	if m.closed || len(m.active) >= 4 {
		m.mu.Unlock()
		return errors.New("CLI metadata is unavailable or busy")
	}
	m.next++
	id := m.next
	m.active[id] = cancel
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.active, id); m.mu.Unlock() }()
	before, err := m.scope(ctx)
	if err != nil {
		return err
	}
	connection, err := m.open(ctx)
	if err != nil {
		return err
	}
	defer connection.release()
	stop := context.AfterFunc(ctx, connection.rpc.Close)
	defer stop()
	value, err := connection.rpc.RequestTimeout("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "filo", "title": "Filo", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, 10*time.Second)
	if err != nil {
		return err
	}
	fields, ok := nativejson.Fields(value)
	home := nativejson.Text(fields["codexHome"])
	if !ok || !filepath.IsAbs(home) {
		return errors.New("CLI metadata native store identity is unavailable")
	}
	if err := m.verify(ctx, home); err != nil {
		return err
	}
	if err := connection.rpc.Notify("initialized", nil); err != nil {
		return err
	}
	if err := apply(codex.NewNativeMetadata(cliMetadataRPC{ctx: ctx, rpc: connection.rpc})); err != nil {
		return err
	}
	after, err := m.scope(ctx)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("CLI metadata native scope changed")
	}
	return ctx.Err()
}

func (m *cliSourceMetadata) Models(ctx context.Context) ([]protocol.Model, error) {
	var result []protocol.Model
	err := m.read(ctx, func(native *codex.NativeMetadata) (err error) { result, err = native.Models(); return err })
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (m *cliSourceMetadata) Usage(ctx context.Context) (protocol.AccountUsage, error) {
	var result protocol.AccountUsage
	err := m.read(ctx, func(native *codex.NativeMetadata) (err error) { result, err = native.Usage(); return err })
	if err != nil {
		return protocol.AccountUsage{}, err
	}
	return result, nil
}

func (m *cliSourceMetadata) Close() {
	m.mu.Lock()
	m.closed = true
	for _, cancel := range m.active {
		cancel()
	}
	m.mu.Unlock()
}
