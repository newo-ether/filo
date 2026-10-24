package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

func sharedProbeSurface(t *testing.T, root string, native *codex.CodexHost, endpoint, token string) (*CLIControlledSessions, func()) {
	t.Helper()
	lease := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(lease.Close)
	registry := filepath.Join(root, "registry")
	identifier, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(registry, identifier)
	if err := prepareCLISharedDirectory(directory); err != nil {
		t.Fatal(err)
	}
	record := cliSharedRecord{URL: endpoint, Token: token, Lease: lease.URL + "/lease", Home: native.CodexHome, Workspace: root}
	if err := SaveExecutorFile(directory, cliSharedRecordName, record); err != nil {
		t.Fatal(err)
	}
	nativeHistory := history.NewNativeHistory(func(context.Context) (string, error) { return native.CodexHome, nil })
	reader, err := codexcli.New(codexcli.Options{History: nativeHistory, Scope: func(context.Context) (string, error) { return "probe-scope", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	surface, err := NewCLIControlledSessions(CLIControlledOptions{Directory: root, Workspace: root,
		Reader: reader, History: nativeHistory, Scope: func(context.Context) (string, error) { return "probe-scope", nil },
		NativeHome: func(context.Context) (string, error) { return native.CodexHome, nil },
		Daemon:     func(context.Context) (*codex.CodexHost, error) { return nil, codexcliUnavailable() }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	surface.sharedFactory.root = registry
	surface.sharedFactory.daemon = nil
	return surface, lease.Close
}

func TestCLISharedCatalogDoesNotReadHistoricalList(t *testing.T) {
	surface, _ := newCLIControlledFixture(t)
	guard := &sharedCatalogGuard{}
	surface.TaskSessions.history = guard
	reader, err := codexcli.New(codexcli.Options{History: guard, Scope: func(context.Context) (string, error) { return "scope", nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	surface.reader = reader
	page, err := surface.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if guard.called {
		t.Fatal("main catalog scanned history")
	}
}

type sharedCatalogGuard struct{ called bool }

func (g *sharedCatalogGuard) List(context.Context, string) (protocol.SessionPage, error) {
	g.called = true
	return protocol.SessionPage{}, nil
}
func (*sharedCatalogGuard) Read(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
	return protocol.ConversationPage{}, nil
}

func TestSharedCLIRejectsEndpointOverrides(t *testing.T) {
	for _, arguments := range [][]string{{"--remote", "ws://127.0.0.1:1"}, {"--remote=x"}, {"--remote-auth-token-env=OTHER"}} {
		if validateSharedCLIArguments(arguments) == nil {
			t.Fatalf("accepted %v", arguments)
		}
	}
}

func TestSharedCLIArgumentsPreservePromptAndOptionValues(t *testing.T) {
	cases := []struct {
		name      string
		arguments []string
		config    []string
	}{
		{"resume identity", []string{"--model", "example", "resume", "help"}, []string{}},
		{"prompt words", []string{"explain", "exec", "help"}, []string{}},
		{"literal prompt", []string{"--", "exec", "--config=literal"}, []string{}},
		{"option value", []string{"--model", "--enable=literal", "hello"}, []string{}},
		{"configuration", []string{"--model=x", "-c", "a=b", "--enable=feature", "--disable", "other", "--strict-config", "resume", "help"}, []string{"-c", "a=b", "--enable=feature", "--disable", "other", "--strict-config"}},
		{"short configuration", []string{"-ca=b", "hello"}, []string{"-ca=b"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSharedCLIArguments(test.arguments); err != nil {
				t.Fatal(err)
			}
			if got := sharedCLIConfigArguments(test.arguments); !reflect.DeepEqual(got, test.config) {
				t.Fatalf("forwarded %v, want %v", got, test.config)
			}
		})
	}
	for _, arguments := range [][]string{{"--model"}, {"--config"}, {"exec", "hello"}, {"--profile=x"}, {"-p", "x"}, {"-px"}} {
		if validateSharedCLIArguments(arguments) == nil {
			t.Fatalf("accepted unsupported arguments %v", arguments)
		}
	}
}

func TestCLISharedCatalogDiscardsExpiredAdmission(t *testing.T) {
	directory := t.TempDir()
	factory := &cliSharedFactory{root: filepath.Join(directory, "absent"), tasks: map[string]cliSharedRecord{taskSessionsTaskID: {}}}
	rows, err := factory.catalog(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if owned, _ := factory.Owns(taskSessionsTaskID); owned {
		t.Fatal("absent registry retained stale authority")
	}
	factory.root = directory
	factory.tasks[taskSessionsTaskID] = cliSharedRecord{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := factory.catalog(ctx); err == nil {
		t.Fatal("cancelled catalog reported success")
	}
	if owned, _ := factory.Owns(taskSessionsTaskID); owned {
		t.Fatal("cancelled discovery retained stale authority")
	}
}

func sharedSurfaceReceipt(t *testing.T, surface *CLIControlledSessions, id, text, client string) protocol.SendReceipt {
	t.Helper()
	access, err := surface.Access(context.Background(), id)
	if err != nil || !access.Capabilities.IdleSend {
		t.Fatalf("shared idle access=%+v err=%v", access, err)
	}
	receipt, err := surface.Send(protocol.WithAccessBinding(context.Background(), access.Binding), id, text, client)
	if err != nil {
		t.Fatalf("production shared send failed without retry: %v", err)
	}
	return receipt
}
