//go:build windows

package service

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
	"github.com/newo-ether/filo/internal/windowsapp"
)

// This opt-in probe uses the installed Desktop runtime and one self-created
// thread. It never starts a turn or mutates an existing thread.
func TestDesktopNativeCatalogManagement(t *testing.T) {
	if os.Getenv("FILO_DESKTOP_MANAGEMENT_PROBE") != "1" {
		t.Skip("explicit self-created Desktop management probe is disabled")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := desktop.NewClient(desktop.Options{Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
		return desktop.DialPipe(ctx, desktop.DefaultPipePath, func(ctx context.Context, pid uint32) error {
			_, err := windowsapp.Inspect(ctx, pid, account.Uid)
			return err
		})
	}})
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("verified Desktop IPC: %v", err)
	}
	executable, err := resolveDesktopRuntime()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "app-server", "--listen", "stdio://")
	command.Dir = processWorkingDirectory()
	command.Env = nativeProcessEnvironment(executable)
	command.SysProcAttr = detachedProcessAttributes()
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		input.Close()
		output.Close()
		t.Fatal(err)
	}
	rpc := codex.NewRpc(output, input)
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	defer func() {
		rpc.Close()
		input.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			// This child has never accepted a turn; no user backend is signaled.
			_ = command.Process.Kill()
			<-done
		}
	}()
	value, err := rpc.RequestTimeout("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "filo", "title": "Filo", "version": "0.2.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, 10*time.Second)
	home, _ := sharedProbeObject(value)["codexHome"].(string)
	if err != nil || !strings.EqualFold(filepath.Clean(home), filepath.Join(account.HomeDir, ".codex")) {
		t.Fatalf("Desktop native store identity unconfirmed: %v", err)
	}
	if err := rpc.Notify("initialized", nil); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	value, err = rpc.RequestTimeout("thread/start", map[string]any{
		"cwd": root, "approvalPolicy": "never", "sandbox": "read-only", "persistExtendedHistory": true,
	}, 10*time.Second)
	if err != nil {
		t.Fatalf("self-created native thread failed; not replayed: %v", err)
	}
	thread := sharedProbeObject(sharedProbeObject(value)["thread"])
	id, _ := thread["id"].(string)
	cwd, _ := thread["cwd"].(string)
	if !uuidPattern.MatchString(id) || !paritySameDirectory(cwd, root) {
		t.Fatal("self-created native thread identity/cwd unconfirmed")
	}
	t.Logf("self-created Desktop thread=%s", id)
	metadata := codex.NewNativeMetadata(desktopProbeRPC{rpc})
	base := &desktopManagementBase{metadata: metadata}
	surface := sessions.New(sessions.Options{IPC: client, Base: base,
		AccessScope: func(context.Context) (string, error) { return "desktop_management_probe", nil }})
	defer surface.Close()
	archived := false
	defer func() {
		if archived || base.archiveAttempted {
			return
		}
		value, err := rpc.RequestTimeout("thread/read", map[string]any{"threadId": id, "includeTurns": false}, 5*time.Second)
		thread := sharedProbeObject(sharedProbeObject(value)["thread"])
		cwd, _ := thread["cwd"].(string)
		if err != nil || thread["id"] != id || !paritySameDirectory(cwd, root) {
			t.Errorf("exact empty-thread cleanup identity unavailable: %v", err)
			return
		}
		if _, err := metadata.Archive(id); err != nil {
			t.Errorf("exact empty-thread cleanup failed; not replayed: %v", err)
		}
	}()
	access, err := surface.CatalogAccess(ctx, id)
	if err != nil || !access.Capabilities.Rename || !access.Capabilities.Archive || access.Capabilities.IdleSend {
		t.Fatalf("catalog management grant: %+v %v", access.Capabilities, err)
	}
	bound := protocol.WithAccessBinding(ctx, access.Binding)
	name := "Filo Desktop management probe " + id[:8]
	renamed, err := surface.Rename(bound, id, name)
	if err != nil || renamed.ID != id || string(renamed.Title) != name {
		t.Fatalf("native rename/readback unconfirmed; not replayed: %v", err)
	}
	cwd, err = surface.Archive(bound, id)
	if err != nil || !paritySameDirectory(cwd, root) {
		t.Fatalf("native archive/broadcast unconfirmed; not replayed: %v", err)
	}
	archived = true
	t.Logf("same-thread native rename/readback and archive/verified-IPC broadcast passed thread=%s", id)
}

type desktopProbeRPC struct{ rpc *codex.Rpc }

func (r desktopProbeRPC) Request(method string, params any) (any, error) {
	return r.rpc.RequestTimeout(method, params, 10*time.Second)
}

type desktopManagementBase struct {
	SessionAPI
	metadata         *codex.NativeMetadata
	archiveAttempted bool
}

func (b *desktopManagementBase) Rename(_ context.Context, id, name string) (protocol.Session, error) {
	return b.metadata.Rename(id, name)
}

func (b *desktopManagementBase) Archive(_ context.Context, id string) (string, error) {
	b.archiveAttempted = true
	return b.metadata.Archive(id)
}
