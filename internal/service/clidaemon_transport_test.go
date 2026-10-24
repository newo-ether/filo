package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

type cliDaemonFixture struct {
	mu       sync.Mutex
	loaded   []any
	cursor   any
	threads  map[string]any
	methods  []string
	after    func(string)
	connects int
	factory  *cliSharedFactory
}

func newCLIDaemonFixture(t *testing.T) *cliDaemonFixture {
	t.Helper()
	f := &cliDaemonFixture{loaded: []any{taskSessionsTaskID}, threads: map[string]any{}}
	f.threads[taskSessionsTaskID] = daemonThread(taskSessionsTaskID, nil, "idle")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := codex.UpgradeWebSocket(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		defer socket.Terminate()
		for {
			body, err := socket.ReadMessage()
			if err != nil {
				return
			}
			var packet map[string]any
			if err := json.Unmarshal(body, &packet); err != nil {
				t.Error(err)
				return
			}
			name, _ := packet["method"].(string)
			params, _ := nativejson.Fields(packet["params"])
			f.mu.Lock()
			f.methods = append(f.methods, name)
			var result any
			switch name {
			case "initialize":
				result = map[string]any{"codexHome": "fixture-home"}
			case "initialized":
				f.mu.Unlock()
				continue
			case "thread/loaded/list":
				result = map[string]any{"data": append([]any{}, f.loaded...), "nextCursor": f.cursor}
			case "thread/read":
				if params["includeTurns"] != false {
					t.Error("catalog hydrated native history")
				}
				result = map[string]any{"thread": f.threads[nativejson.Text(params["threadId"])]}
			case "thread/turns/list":
				if params["limit"] != float64(1) || params["sortDirection"] != "desc" || params["itemsView"] != "summary" {
					t.Error("live state did not request one bounded summary")
				}
				result = map[string]any{"data": []any{map[string]any{"id": "native-turn", "status": "completed"}}}
			default:
				t.Errorf("unexpected native operation %q", name)
			}
			after := f.after
			f.mu.Unlock()
			if after != nil {
				after(name)
			}
			response, err := json.Marshal(map[string]any{"id": packet["id"], "result": result})
			if err != nil || socket.WriteText(response) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	f.factory = &cliSharedFactory{tasks: map[string]cliSharedRecord{}, daemon: func(ctx context.Context) (*codex.CodexHost, error) {
		f.mu.Lock()
		f.connects++
		f.mu.Unlock()
		return codex.ConnectCodexHost(ctx, strings.Replace(server.URL, "http:", "ws:", 1), codex.HostOptions{Timeout: time.Second})
	}}
	return f
}

func daemonThread(id string, parent any, status string) any {
	return map[string]any{"id": id, "parentThreadId": parent, "name": "native terminal", "cwd": "fixture-workspace",
		"updatedAt": 123, "status": map[string]any{"type": status}}
}

func TestCLIDaemonCatalogAndFollowUseOnlyLoadedMainThreads(t *testing.T) {
	f := newCLIDaemonFixture(t)
	child := "44444444-4444-4444-8444-444444444444"
	unloaded := "22222222-2222-4222-8222-222222222222"
	mismatch := "33333333-3333-4333-8333-333333333333"
	f.loaded = append(f.loaded, child, unloaded, mismatch)
	f.threads[child] = daemonThread(child, taskSessionsTaskID, "active")
	f.threads[unloaded] = daemonThread(unloaded, nil, "notLoaded")
	f.threads[mismatch] = daemonThread(taskSessionsTaskID, nil, "idle")
	rows, err := f.factory.catalog(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ID != taskSessionsTaskID || string(rows[0].Title) != "native terminal" {
		t.Fatalf("catalog=%+v err=%v", rows, err)
	}
	for _, id := range []string{child, unloaded, mismatch} {
		if owned, _ := f.factory.Owns(id); owned {
			t.Fatalf("invalid thread admitted: %s", id)
		}
	}
	connection, err := f.factory.Follow(context.Background(), taskSessionsTaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	stateReader := connection.(interface{ TaskState(string) (any, error) })
	value, err := stateReader.TaskState(taskSessionsTaskID)
	fields, _ := nativejson.Fields(value)
	turn, _ := nativejson.Fields(fields["turn"])
	if err != nil || turn["id"] != "native-turn" || turn["status"] != "completed" {
		t.Fatalf("state=%v err=%v", value, err)
	}
	if _, err := stateReader.TaskState(child); err == nil {
		t.Fatal("follow accepted a different native task")
	}
	connection.Close()
	// Closing an observation connection must leave the native endpoint usable.
	if rows, err := f.factory.catalog(context.Background()); err != nil || len(rows) != 1 {
		t.Fatalf("observer close affected native owner: rows=%v err=%v", rows, err)
	}
}

func TestCLIDaemonFollowRejectsUnloadedThreadWithoutResume(t *testing.T) {
	f := newCLIDaemonFixture(t)
	if _, err := f.factory.catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.loaded = nil
	f.mu.Unlock()
	if connection, err := f.factory.Follow(context.Background(), taskSessionsTaskID); connection != nil || err == nil {
		t.Fatal("unloaded native task was attached or resumed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"initialize", "initialized", "thread/loaded/list", "thread/read", "initialize", "initialized", "thread/loaded/list"}
	if !reflect.DeepEqual(f.methods, want) {
		t.Fatalf("native operations=%v", f.methods)
	}
}

func TestCLIDaemonCatalogRejectsUnboundedLoadedPages(t *testing.T) {
	for _, paginated := range []bool{false, true} {
		t.Run(map[bool]string{false: "oversized", true: "cursor"}[paginated], func(t *testing.T) {
			f := newCLIDaemonFixture(t)
			if paginated {
				f.cursor = "more"
			} else {
				for len(f.loaded) <= 30 {
					f.loaded = append(f.loaded, taskSessionsTaskID)
				}
			}
			f.factory.tasks[taskSessionsTaskID] = cliSharedRecord{}
			if _, err := f.factory.catalog(context.Background()); err == nil {
				t.Fatal("unbounded native page admitted")
			}
			if owned, _ := f.factory.Owns(taskSessionsTaskID); owned {
				t.Fatal("failed catalog retained authority")
			}
		})
	}
}

func TestCLIDaemonScopeChangeDuringReadNeverPublishesAuthority(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(map[bool]string{false: "catalog", true: "follow"}[follow], func(t *testing.T) {
			f := newCLIDaemonFixture(t)
			f.factory.tasks[taskSessionsTaskID] = cliSharedRecord{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = protocol.WithScopeValidation(ctx, func(ctx context.Context) error { return ctx.Err() })
			f.after = func(method string) {
				if method == "thread/loaded/list" {
					cancel()
				}
			}
			if follow {
				connection, err := f.factory.Follow(ctx, taskSessionsTaskID)
				if connection != nil {
					connection.Close()
					t.Fatal("follow returned authority after scope changed")
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				if _, err := f.factory.catalog(ctx); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if owned, _ := f.factory.Owns(taskSessionsTaskID); owned {
					t.Fatal("catalog published authority after scope changed")
				}
			}
		})
	}
}
