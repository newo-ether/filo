package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
)

func TestCLIDaemonMissingSocketDoesNotInitializeNativeRuntime(t *testing.T) {
	root := t.TempDir()
	called := false
	connector := existingCLIDaemon("", "fixture-user", func(context.Context) (string, error) { return root, nil },
		func(context.Context, codex.LocalDaemonIdentity) error { called = true; return nil })
	if host, err := connector(context.Background()); host != nil || err == nil || called {
		t.Fatal("missing native socket admitted a connection")
	}
	files, err := filepath.Glob(filepath.Join(root, "*"))
	if err != nil || len(files) != 0 {
		t.Fatal("discovery wrote native state")
	}
}

func TestCLIDaemonDiscoveryDropsAdmissionWithoutWrapperFallback(t *testing.T) {
	calls := 0
	factory := &cliSharedFactory{root: t.TempDir(), tasks: map[string]cliSharedRecord{taskSessionsTaskID: {}},
		daemon: func(context.Context) (*codex.CodexHost, error) { calls++; return nil, errors.New("native offline") }}
	rows, err := factory.catalog(context.Background())
	if err != nil || len(rows) != 0 || calls != 1 {
		t.Fatalf("rows=%v err=%v calls=%d", rows, err, calls)
	}
	if owned, _ := factory.Owns(taskSessionsTaskID); owned {
		t.Fatal("offline daemon retained mutation authority")
	}
	if connection, err := factory.Follow(context.Background(), taskSessionsTaskID); connection != nil || err == nil || calls != 1 {
		t.Fatal("unadmitted native thread reconnected or fell back to a wrapper")
	}
}
