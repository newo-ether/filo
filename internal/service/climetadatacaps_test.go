package service

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/protocol"
)

func TestCodexCLIMetadataCapsMatchNativeAdapterAndRemainReadOnly(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "history-only", true: "configured-metadata"}[configured], func(t *testing.T) {
			caps := codexCLIMetadataCaps(configured)
			if !reflect.DeepEqual(caps, codexcli.MetadataCapabilities(configured)) {
				t.Fatal("private adapter and service capabilities differ")
			}
			if !caps.History || !caps.LazyMessages || caps.HistoryMode != "bounded" || caps.Models != configured || caps.Usage != configured {
				t.Fatalf("wrong metadata capabilities: %+v", caps)
			}
			if caps.Live || caps.Create || caps.IdleSend || caps.ActiveSend != "unsupported" || caps.Stop || caps.Settings == nil || len(caps.Settings) != 0 || caps.Rename || caps.Archive || caps.Uploads {
				t.Fatalf("metadata acquired execution: %+v", caps)
			}
			if normalized, err := normalizedCaps(caps); err != nil || !reflect.DeepEqual(caps, normalized) {
				t.Fatalf("metadata capability contract is invalid: %+v %v", normalized, err)
			}
			if projected := intersectCaps(caps, codexCLIMetadataCaps(true)); !reflect.DeepEqual(projected, caps) {
				t.Fatal("public metadata ceiling expanded private capabilities")
			}
			caps.Settings = append(caps.Settings, "model")
			if len(codexCLIMetadataCaps(configured).Settings) != 0 {
				t.Fatal("caller modified later metadata capability results")
			}
		})
	}
}

func TestCodexCLIMetadataCapsGateEncryptedReadRoutesWithoutExecution(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-endpoint", true: "configured-endpoint"}[configured], func(t *testing.T) {
			native := newAdapterFixture()
			native.models = []protocol.Model{{ID: "native-model", ReasoningEfforts: []string{"high"}}}
			usageCalls := 0
			adapter := fixtureAdapter(codexCLISource, native, nil)
			adapter.Capabilities = codexCLIMetadataCaps(configured)
			adapter.Usage = func(context.Context) (protocol.AccountUsage, error) {
				usageCalls++
				return protocol.AccountUsage{Limits: []protocol.UsageLimit{{ID: "native-limit", Name: "Native account"}}}, nil
			}
			_, base, client := encryptedAdapterServer(t, []Adapter{adapter}, nil)
			prefix := base + "/v1/adapters/codex-cli"
			for _, route := range []string{"models", "usage"} {
				status, data := adapterRequest(t, client, http.MethodGet, prefix+"/"+route, nil, nil)
				if !configured {
					if status != http.StatusNotImplemented {
						t.Fatalf("unconfigured %s route returned %d %s", route, status, data)
					}
					continue
				}
				if status != http.StatusOK {
					t.Fatalf("configured %s route returned %d %s", route, status, data)
				}
				if route == "models" {
					var response modelsResponse
					if json.Unmarshal(data, &response) != nil || len(response.Models) != 1 || response.Models[0].ID != "native-model" {
						t.Fatalf("native model metadata changed: %s", data)
					}
				} else {
					var response protocol.AccountUsage
					if json.Unmarshal(data, &response) != nil || len(response.Limits) != 1 || response.Limits[0].ID != "native-limit" {
						t.Fatalf("native usage metadata changed: %s", data)
					}
				}
			}
			binding := fixtureBinding(t, client, base, codexCLISource)
			for _, suffix := range []string{"sessions", "sessions/" + payloadSession + "/messages", "sessions/" + payloadSession + "/stop",
				"sessions/" + payloadSession + "/settings", "sessions/" + payloadSession + "/rename", "sessions/" + payloadSession + "/archive", "uploads"} {
				status, _ := adapterRequest(t, client, http.MethodPost, prefix+"/"+suffix, map[string]any{"accessBinding": binding}, nil)
				if status != http.StatusNotImplemented {
					t.Fatalf("metadata source admitted mutation %s: %d", suffix, status)
				}
			}
			status, _ := adapterRequest(t, client, http.MethodGet, prefix+"/sessions/"+payloadSession+"/events", nil, nil)
			if status != http.StatusNotImplemented || native.dispatches != 0 || usageCalls != map[bool]int{false: 0, true: 1}[configured] {
				t.Fatalf("metadata admitted live/execution or wrong usage calls: status=%d dispatches=%d usage=%d", status, native.dispatches, usageCalls)
			}
		})
	}
}

func TestCodexCLIConfiguredMetadataFailureDoesNotDisableHistory(t *testing.T) {
	cli, err := codexcli.New(codexcli.Options{History: &cliMetadataHistory{},
		Scope:      func(context.Context) (string, error) { return "verified-scope", nil },
		SocketPath: filepath.Join(t.TempDir(), "missing.sock"), UserID: "fixture-user",
		VerifyDaemon: func(context.Context, codex.LocalDaemonIdentity) error {
			t.Fatal("missing daemon reached identity verification")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	adapter := Adapter{ID: codexCLISource, Family: "codex", Origin: "cli", NativeScopeID: "verified-scope",
		Sessions: cli, Capabilities: codexCLIMetadataCaps(true), Usage: cli.Usage,
		Status: func() AdapterStatus { return sourceStatus("disconnected", "native_connection_unavailable") }}
	_, base, client := encryptedAdapterServer(t, []Adapter{adapter}, nil)
	for _, route := range []string{"models", "usage"} {
		status, _ := adapterRequest(t, client, http.MethodGet, base+"/v1/adapters/codex-cli/"+route, nil, nil)
		if status == http.StatusOK || status == http.StatusNotImplemented {
			t.Fatalf("configured but unavailable metadata was accepted or hidden: %s %d", route, status)
		}
	}
	status, data := adapterRequest(t, client, http.MethodGet, base+"/v1/adapters/codex-cli/sessions", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("metadata failure disabled history: %d %s", status, data)
	}
}

type cliMetadataHistory struct{}

func (*cliMetadataHistory) List(context.Context, string) (protocol.SessionPage, error) {
	return protocol.SessionPage{Sessions: []protocol.Session{}}, nil
}

func (*cliMetadataHistory) Read(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
	return protocol.ConversationPage{Messages: []protocol.Message{}}, nil
}
