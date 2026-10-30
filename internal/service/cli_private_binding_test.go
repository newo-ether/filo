package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestCLISettingsBindingSurvivesPrivateDiscoveryAndEncryptedGateway(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "owned"
		if shared {
			name = "shared-daemon"
		}
		t.Run(name, func(t *testing.T) {
			surface, fixture := newCLIControlledFixture(t)
			metadata, _ := newCLIMetadataFixture(t)
			surface.metadata = metadata
			if _, _, err := fixture.sessions.Create(context.Background(), "first", "client", protocol.SessionSettings{}); err != nil {
				t.Fatal(err)
			}
			if shared {
				daemon := newCLIDaemonFixture(t)
				surface.shared = fixture.sessions
				surface.sharedFactory = daemon.factory
			}
			adapter := Adapter{ID: codexCLISource, Family: "codex", Origin: "cli", Scope: surface.scope,
				Sessions: surface, Capabilities: surface.Capabilities(), Usage: surface.Usage,
				Status: func() AdapterStatus {
					return surface.nativeStatus(context.Background(), func() (string, error) { return "fixture-codex", nil })
				}}
			private, err := NewServer(ServerOptions{Token: strings.Repeat("b", 64),
				Identity: &ServiceIdentity{SessionMode: "standalone"}, Adapters: []Adapter{adapter}})
			if err != nil {
				t.Fatal(err)
			}
			privateServer := httptest.NewServer(private)
			t.Cleanup(privateServer.Close)
			worker := newUserWorkerFixture(t, privateServer.URL, StaticUserWorkerToken(strings.Repeat("b", 64)), userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
			source := newUserSource(worker, codexCLISource, nil)
			publicAdapter := Adapter{ID: codexCLISource, Family: "codex", Origin: "cli", Scope: source.scope,
				Sessions: source, Capabilities: surface.Capabilities(), CapabilitySource: source.capabilities}
			_, base, client := encryptedAdapterServer(t, []Adapter{publicAdapter}, nil)
			path := base + "/v1/adapters/codex-cli/sessions/" + taskSessionsTaskID
			for _, route := range []string{"/v1/info", "/v1/adapters/codex-cli/sessions"} {
				if status, data := adapterRequest(t, client, "GET", base+route, nil, nil); status != 200 {
					t.Fatalf("initial discovery %s: status=%d data=%s", route, status, data)
				}
			}
			status, data := adapterRequest(t, client, "GET", path, nil, nil)
			var page protocol.ConversationPage
			if status != 200 || json.Unmarshal(data, &page) != nil || page.Access == nil || len(page.Access.Capabilities.Settings) == 0 {
				t.Fatalf("initial access unavailable: status=%d data=%s", status, data)
			}
			binding := page.Access.Binding
			for _, route := range []string{"/v1/info", "/v1/adapters/codex-cli/sessions", "/v1/info", "/v1/adapters/codex-cli/models"} {
				if status, data := adapterRequest(t, client, "GET", base+route, nil, nil); status != 200 {
					t.Fatalf("fresh discovery %s: status=%d data=%s", route, status, data)
				}
			}
			status, data = adapterRequest(t, client, "POST", path+"/settings",
				map[string]any{"effort": "low", "accessBinding": binding}, nil)
			if status != 200 {
				t.Fatalf("unchanged owner binding rejected: status=%d data=%s", status, data)
			}
			status, data = adapterRequest(t, client, "GET", path, nil, nil)
			if status != 200 || json.Unmarshal(data, &page) != nil || page.Runtime == nil || !page.Runtime.Effort.Known || page.Runtime.Effort.Value == nil || *page.Runtime.Effort.Value != "low" {
				t.Fatalf("native effort readback missing: status=%d data=%s", status, data)
			}
			if fixture.peer.callCount("thread/settings/update") != 1 || fixture.peer.callCount("thread/resume") != 0 || fixture.peer.callCount("turn/start") != 1 {
				t.Fatal("settings replayed or acquired another writer", fixture.peer.calls())
			}
		})
	}
}

func TestCLISharedBindingDuringConcurrentCatalogRefresh(t *testing.T) {
	ctx := context.Background()
	surface, _ := newCLIControlledFixture(t)
	daemon := newCLIDaemonFixture(t)
	surface.sharedFactory = daemon.factory
	shared := newTaskSessionsFixture(t)
	if _, _, err := shared.sessions.Create(ctx, "first", "client", protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	// The shared connection and binding secret are independent of the owned
	// executor. Catalog discovery uses the real daemon registry.
	surface.shared = shared.sessions
	page, err := surface.liveCatalog(ctx, "")
	if err != nil || len(page.Sessions) != 1 || !surface.isShared(taskSessionsTaskID) {
		t.Fatalf("shared admission: page=%+v err=%v", page, err)
	}
	private, err := NewServer(ServerOptions{Token: strings.Repeat("b", 64),
		Identity: &ServiceIdentity{SessionMode: "standalone"}, Adapters: []Adapter{{
			ID: codexCLISource, Family: "codex", Origin: "cli", Scope: surface.scope,
			Sessions: surface, Capabilities: surface.Capabilities(),
			// Isolate final access validation from the descriptor's own discovery.
			Status: func() AdapterStatus { return AdapterStatus{State: "connected"} },
		}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(private)
	t.Cleanup(server.Close)
	worker := newUserWorkerFixture(t, server.URL, StaticUserWorkerToken(strings.Repeat("b", 64)), userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
	source := newUserSource(worker, codexCLISource, nil)
	access, err := source.Access(ctx, taskSessionsTaskID)
	if err != nil || !access.Capabilities.Live || len(access.Capabilities.Settings) == 0 {
		t.Fatalf("initial shared access: access=%+v err=%v", access, err)
	}
	ctx = protocol.WithNativeScopeID(ctx, access.NativeScopeID)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	daemon.mu.Lock()
	daemon.after = func(method string) {
		if method == "thread/loaded/list" {
			enteredOnce.Do(func() { close(entered) })
			<-release
		}
	}
	daemon.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := surface.liveCatalog(context.Background(), "")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("catalog did not reach the discovery window")
	}
	// The actual shared task connection stays alive while discovery is paused.
	// No native setting or input is dispatched by this final-validation probe.
	during := source.ValidateAccess(ctx, taskSessionsTaskID, access.Binding)
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("catalog did not finish after release")
	}
	if err := source.ValidateAccess(ctx, taskSessionsTaskID, access.Binding); err != nil {
		t.Fatalf("unchanged shared binding did not recover: %v", err)
	}
	if during != nil {
		t.Fatalf("unchanged shared owner rejected during successful discovery: %v", during)
	}
}
