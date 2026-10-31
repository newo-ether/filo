package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

type cliCreateBoundary struct {
	SessionAPI
	creates atomic.Int32
}

func (s *cliCreateBoundary) Create(ctx context.Context, text, client string, settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	s.creates.Add(1)
	return s.SessionAPI.Create(ctx, text, client, settings)
}

func (s *cliCreateBoundary) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	return s.SessionAPI.(SessionAccessProvider).Access(ctx, id)
}

func (s *cliCreateBoundary) ValidateAccess(ctx context.Context, id, binding string) error {
	return s.SessionAPI.(SessionAccessProvider).ValidateAccess(ctx, id, binding)
}

func TestCLICreationRefusedBeforeNativeAllocation(t *testing.T) {
	cli, fixture := newCLIControlledFixture(t)
	if cli.Capabilities().Create {
		t.Fatal("Codex CLI advertises creation")
	}
	if _, _, err := cli.Create(context.Background(), "first", "new-client", protocol.SessionSettings{}); err == nil {
		t.Fatal("Codex CLI accepted creation")
	}
	if opens, _ := fixture.factory.counts(); opens != 0 || fixture.peer.callCount("thread/start") != 0 || fixture.peer.callCount("turn/start") != 0 {
		t.Fatal("rejected Codex CLI creation allocated a native writer or sent input")
	}
	root := filepath.Join(t.TempDir(), "broker")
	launches := 0
	claude, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: root, Workspace: t.TempDir(),
		NativeScopeID: "scope", Launch: func(string) error { launches++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer claude.Close()
	if claude.Capabilities().Create {
		t.Fatal("Claude Code advertises creation")
	}
	if _, _, err := claude.Create(context.Background(), "first", "new-client", protocol.SessionSettings{}); err == nil {
		t.Fatal("Claude Code accepted creation")
	}
	if launches != 0 {
		t.Fatal("rejected Claude Code creation started a broker")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("rejected Claude Code creation touched the broker directory: %v", err)
	}
}

func TestCLICreationUnsupportedAcrossWorkerAndEncryptedGateway(t *testing.T) {
	for _, id := range []string{codexCLISource, claudeCodeSource} {
		t.Run(id, func(t *testing.T) {
			cli, fixture := newCLIControlledFixture(t)
			// Seed previously accepted work below the newly disabled source entry.
			// Its existing owner must remain controllable after this policy change.
			if _, _, err := fixture.sessions.Create(context.Background(), "accepted", "old-client", protocol.SessionSettings{}); err != nil {
				t.Fatal(err)
			}
			var source SessionAPI = cli
			caps := cli.Capabilities()
			family := "codex"
			if id == claudeCodeSource {
				directory := t.TempDir()
				config := claudeBrokerConfig{Token: testToken, Workspace: t.TempDir(), Scope: "scope"}
				broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request claudeBrokerRequest
					if r.Header.Get("Authorization") != "Bearer "+testToken || json.NewDecoder(r.Body).Decode(&request) != nil {
						t.Error("invalid existing-broker request")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					response := claudeBrokerResponse{TimestampVersion: 1}
					switch request.Operation {
					case "read":
						page, err := cli.Read(context.Background(), request.ID, "", false, nil)
						if err != nil {
							t.Error(err)
						}
						response.Page = &page
					case "stop":
						if err := cli.Stop(context.Background(), request.ID, request.TurnID); err != nil {
							t.Error(err)
						}
					default:
						t.Errorf("unexpected existing-broker operation %q", request.Operation)
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer broker.Close()
				if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
					t.Fatal(err)
				}
				if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: broker.URL, PID: os.Getpid()}); err != nil {
					t.Fatal(err)
				}
				owned, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: directory,
					Workspace: config.Workspace, NativeScopeID: config.Scope})
				if err != nil {
					t.Fatal(err)
				}
				defer owned.Close()
				source, caps, family = owned, owned.Capabilities(), "claude"
			}
			native := &cliCreateBoundary{SessionAPI: source}
			private, err := NewServer(ServerOptions{Token: testToken, Identity: &ServiceIdentity{SessionMode: "standalone"}, Adapters: []Adapter{{
				ID: id, Family: family, Origin: "cli", NativeScopeID: "scope", Sessions: native, Capabilities: caps,
				Status: func() AdapterStatus { return AdapterStatus{State: "connected"} },
			}}})
			if err != nil {
				t.Fatal(err)
			}
			endpoint := httptest.NewServer(private)
			defer endpoint.Close()
			worker, err := NewUserWorker(endpoint.URL, StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			legacy := newAdapterFixture()
			adapters := gatewaySourceAdapters([]string{id}, worker, func(string, string) {}, Adapter{}, nil)
			if len(adapters) != 1 || adapters[0].Capabilities.Create {
				t.Fatal("gateway ceiling permits CLI creation")
			}
			_, base, client := encryptedAdapterServer(t, adapters, legacy)
			for _, target := range []struct {
				name, base string
				client     *http.Client
				headers    map[string]string
			}{
				{"worker", endpoint.URL, endpoint.Client(), map[string]string{"Authorization": "Bearer " + testToken}},
				{"encrypted-gateway", base, client, nil},
			} {
				t.Run(target.name, func(t *testing.T) {
					status, data := adapterRequest(t, target.client, http.MethodGet, target.base+"/v1/info", nil, target.headers)
					var info infoResponse
					if status != http.StatusOK || json.Unmarshal(data, &info) != nil || len(info.Adapters) != 1 {
						t.Fatalf("discovery status=%d", status)
					}
					descriptor := info.Adapters[0]
					if descriptor.ID != id || descriptor.State != "connected" || descriptor.Capabilities.Create ||
						!descriptor.Capabilities.IdleSend || !descriptor.Capabilities.Stop || !descriptor.Capabilities.Live ||
						!reflect.DeepEqual(descriptor.Capabilities.Settings, caps.Settings) {
						t.Fatalf("CLI capabilities changed beyond creation: %+v", descriptor)
					}
					status, _ = adapterRequest(t, target.client, http.MethodPost, target.base+"/v1/adapters/"+id+"/sessions",
						map[string]any{"nativeScopeId": descriptor.NativeScopeID, "text": "first", "clientId": "new-client"}, target.headers)
					if status != http.StatusNotImplemented || native.creates.Load() != 0 || legacy.dispatches != 0 {
						t.Fatalf("create status=%d native=%d fallback=%d", status, native.creates.Load(), legacy.dispatches)
					}
					path := target.base + "/v1/adapters/" + id + "/sessions/" + taskSessionsTaskID
					status, data = adapterRequest(t, target.client, http.MethodGet, path, nil, target.headers)
					var page protocol.ConversationPage
					if status != http.StatusOK || json.Unmarshal(data, &page) != nil || page.Access == nil || !page.Access.Capabilities.Stop {
						t.Fatalf("existing owner read status=%d", status)
					}
					binding := page.Access.Binding
					if id == codexCLISource {
						status, data = adapterRequest(t, target.client, http.MethodPost, path+"/messages",
							map[string]any{"accessBinding": binding, "text": "adjust", "clientId": payloadSession}, target.headers)
						if status != http.StatusAccepted {
							t.Fatalf("existing active Send status=%d body=%s", status, data)
						}
					}
					t.Logf("create=false, POST sessions=501, native creates=0; existing owner readable")
				})
			}
			if fixture.peer.callCount("thread/start") != 1 || fixture.peer.callCount("turn/start") != 1 {
				t.Fatal("CLI creation attempts changed previously accepted native work")
			}
			if err := source.Stop(context.Background(), taskSessionsTaskID, taskSessionsTurnID); err != nil {
				t.Fatalf("existing accepted execution lost Stop: %v", err)
			}
		})
	}
}
