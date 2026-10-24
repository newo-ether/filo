package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
)

func TestCLIControlledFactorySeparatesSourceProvenance(t *testing.T) {
	fixture := newExecutorFactoryFixture(t)
	cli, err := NewTaskExecutorFactory(TaskExecutorFactoryOptions{Directory: fixture.directory,
		Workspace: fixture.directory, Source: codexCLISource, Launch: fixture.start})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	host, err := cli.Open(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	result, err := host.Rpc.Request("thread/start", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	id := factoryCreatedID(t, result)
	owned, err := cli.Owns(id)
	if err != nil || !owned {
		t.Fatalf("CLI ownership=%v err=%v", owned, err)
	}
	if len(fixture.configs) != 1 || fixture.configs[0].Source != codexCLISource {
		t.Fatal("missing CLI source marker")
	}
	desktop, err := NewTaskExecutorFactory(TaskExecutorFactoryOptions{Directory: fixture.directory,
		Workspace: fixture.directory, Launch: fixture.start})
	if err != nil {
		t.Fatal(err)
	}
	defer desktop.Close()
	if owned, err := desktop.Owns(id); err != nil || owned {
		t.Fatalf("Desktop inherited CLI provenance: %v %v", owned, err)
	}
	if _, err := desktop.Open(context.Background(), &id); err == nil {
		t.Fatal("Desktop acquired CLI writer")
	}
	original := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if _, err := cli.Open(sessions.WithIdleRecovery(context.Background(), original), &original); err == nil {
		t.Fatal("CLI admitted unowned history through idle recovery")
	}
	if len(fixture.configs) != 1 {
		t.Fatal("rejected cross-source/original requests launched a writer")
	}
}

func TestCLIControlledExecutorConfigValidatesSource(t *testing.T) {
	config := ExecutorConfig{Token: strings.Repeat("a", 64), NativeToken: strings.Repeat("b", 64), Workspace: t.TempDir()}
	for _, source := range []string{"", codexDesktopSource, codexCLISource, "claude-code", "other"} {
		config.Source = source
		var value any
		if err := json.Unmarshal(Marshal(config), &value); err != nil {
			t.Fatal(err)
		}
		got, err := ParseExecutorConfig(value)
		valid := source == "" || source == codexDesktopSource || source == codexCLISource
		if valid && (err != nil || got.Source != source) {
			t.Fatalf("source %q: %+v %v", source, got, err)
		}
		if !valid && err == nil {
			t.Fatalf("admitted unsupported source %q", source)
		}
	}
}

func newCLIControlledFixture(t *testing.T) (*CLIControlledSessions, *taskSessionsFixture) {
	t.Helper()
	fixture := newTaskSessionsFixture(t)
	reader, err := codexcli.New(codexcli.Options{History: fixture.history, Scope: func(context.Context) (string, error) { return "scope", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	root := t.TempDir()
	executorID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	executor := filepath.Join(root, "executors", executorID)
	if err := os.MkdirAll(executor, 0700); err != nil {
		t.Fatal(err)
	}
	config := ExecutorConfig{Token: strings.Repeat("a", 64), NativeToken: strings.Repeat("b", 64), Workspace: root, Source: codexCLISource}
	if err := writeExclusive(filepath.Join(executor, executorConfigName), Marshal(config)); err != nil {
		t.Fatal(err)
	}
	if err := RecordCreatedTask(executor, taskSessionsTaskID); err != nil {
		t.Fatal(err)
	}
	factory, err := NewTaskExecutorFactory(TaskExecutorFactoryOptions{Directory: root, Workspace: root, Source: codexCLISource})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	return &CLIControlledSessions{TaskSessions: fixture.sessions, reader: reader, factory: factory,
		scope: func(context.Context) (string, error) { return "scope", nil }}, fixture
}

func TestCLIControlledUsesExactNativeTurns(t *testing.T) {
	surface, fixture := newCLIControlledFixture(t)
	ctx := context.Background()
	created, receipt, err := surface.Create(ctx, "hello", "client", protocol.SessionSettings{})
	if err != nil || created.ID != taskSessionsTaskID || receipt.TurnID != taskSessionsTurnID {
		t.Fatalf("create=%+v receipt=%+v err=%v", created, receipt, err)
	}
	access, err := surface.Access(ctx, created.ID)
	if err != nil || !access.Capabilities.Stop || access.Capabilities.ActiveSend != "native-steer" {
		t.Fatalf("access=%+v err=%v", access, err)
	}
	if _, err := surface.Send(protocol.WithAccessBinding(ctx, access.Binding), created.ID, "adjust", "steer"); err != nil {
		t.Fatal(err)
	}
	if err := surface.Stop(ctx, created.ID, "other-turn"); err == nil {
		t.Fatal("stopped wrong native turn")
	}
	if fixture.peer.callCount("turn/interrupt") != 0 {
		t.Fatal("wrong-turn stop was dispatched")
	}
	if err := surface.Stop(ctx, created.ID, taskSessionsTurnID); err != nil {
		t.Fatal(err)
	}
	if caps := surface.Capabilities(); !caps.Rename || !caps.Archive || caps.Models || caps.Usage {
		t.Fatalf("capabilities: %+v", caps)
	}
	unowned := "11111111-2222-4333-8444-555555555555"
	if _, err := surface.Send(ctx, unowned, "unowned", "client"); !errors.Is(err, codexcli.ErrUnsupported) {
		t.Fatalf("unowned send: %v", err)
	}
	if _, err := surface.Rename(ctx, unowned, "x"); !errors.Is(err, codexcli.ErrUnsupported) {
		t.Fatalf("unowned rename: %v", err)
	}
	if _, err := surface.Archive(ctx, unowned); !errors.Is(err, codexcli.ErrUnsupported) {
		t.Fatalf("unowned archive: %v", err)
	}
	access, err = surface.Access(ctx, created.ID)
	if err != nil || !access.Capabilities.Rename || !access.Capabilities.Archive {
		t.Fatalf("owned access=%+v err=%v", access, err)
	}
	if _, err := surface.Rename(ctx, created.ID, "Renamed"); err != nil {
		t.Fatalf("owned rename: %v", err)
	}
	if cwd, err := surface.Archive(ctx, created.ID); err != nil || cwd != "/fixture" {
		t.Fatalf("owned archive cwd=%q err=%v", cwd, err)
	}
	if fixture.peer.callCount("thread/name/set") != 1 || fixture.peer.callCount("thread/archive") != 1 {
		t.Fatal("owned rename/archive not dispatched exactly once on the native owner")
	}
	if fixture.peer.callCount("thread/start") != 1 || fixture.peer.callCount("turn/steer") != 1 {
		t.Fatal("native calls replayed or lost")
	}
}

func TestCLIControlledStatusRequiresNativeEvidence(t *testing.T) {
	surface, _ := newCLIControlledFixture(t)
	available := func() (string, error) { return "verified-codex.exe", nil }
	if status := surface.nativeStatus(context.Background(), available); status.State != "disconnected" {
		t.Fatalf("executable discovery became online: %+v", status)
	}
	missing := func() (string, error) { return "", errors.New("missing") }
	if status := surface.nativeStatus(context.Background(), missing); status.State != "unavailable" {
		t.Fatalf("missing runtime: %+v", status)
	}
	if _, _, err := surface.Create(context.Background(), "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	if status := surface.nativeStatus(context.Background(), available); status.State != "connected" {
		t.Fatalf("verified active executor not online: %+v", status)
	}
	surface.Close()
	if status := surface.nativeStatus(context.Background(), available); status.State == "connected" {
		t.Fatal("closed executor client retained online status")
	}
}

func TestCLIControlledRuntimeNeverFallsBackToDesktop(t *testing.T) {
	fixture := newExecutorProcessFixture(t)
	fixture.config.Source = codexCLISource
	fixture.writeConfig(nil)
	cliError := errors.New("native CLI unavailable")
	calledDesktop := false
	err := RunTaskExecutorWith(ExecutorProcessOptions{Directory: fixture.directory,
		DesktopRuntime: func() (string, error) { calledDesktop = true; return "desktop", nil },
		CLIRuntime:     func() (string, error) { return "", cliError },
	})
	if !errors.Is(err, cliError) || calledDesktop {
		t.Fatalf("CLI error=%v Desktop fallback=%v", err, calledDesktop)
	}
}

func TestCLIControlledRuntimeRefusesShimsAndDesktopAndMissingTransport(t *testing.T) {
	for _, path := range []string{"codex.exe", filepath.Join(t.TempDir(), "codex.cmd"), filepath.Join(t.TempDir(), "resources", "codex.exe")} {
		if _, err := verifyCLIRuntime(path); err == nil {
			t.Fatalf("admitted %q", path)
		}
	}
	if cliRuntimeHelpSupported("codex app-server --listen") {
		t.Fatal("admitted unauthenticated-only runtime")
	}
	if !cliRuntimeHelpSupported("codex app-server --listen --ws-auth capability-token --ws-token-file") {
		t.Fatal("refused supported transport")
	}
}
