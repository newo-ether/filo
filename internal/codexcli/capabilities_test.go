package codexcli

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

func TestConfiguredMetadataCapabilitiesDoNotInspectNativeState(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "history-only", true: "metadata-endpoint"}[configured], func(t *testing.T) {
			var scopeCalls, daemonCalls atomic.Int32
			history := &historyFixture{}
			options := Options{History: history, Scope: func(context.Context) (string, error) {
				scopeCalls.Add(1)
				return "verified-scope", nil
			}}
			if configured {
				options.SocketPath = filepath.Join(t.TempDir(), "missing.sock")
				options.UserID = "fixture-user"
				options.VerifyDaemon = func(context.Context, codex.LocalDaemonIdentity) error {
					daemonCalls.Add(1)
					return nil
				}
			}
			a, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			caps := a.Capabilities()
			assertMetadataReadCapabilities(t, caps, configured)
			if scopeCalls.Load() != 0 || daemonCalls.Load() != 0 || history.calls.Load() != 0 {
				t.Fatal("capability discovery accessed native state")
			}
			caps.Settings = append(caps.Settings, "model")
			assertMetadataReadCapabilities(t, a.Capabilities(), configured)
			list, err := a.List(context.Background(), "")
			if err != nil || len(list.Sessions) != 1 || list.Sessions[0].Access == nil {
				t.Fatalf("catalog: %+v %v", list, err)
			}
			page, err := a.Read(context.Background(), firstID, "", false, nil)
			if err != nil || page.Access == nil {
				t.Fatalf("history: %+v %v", page, err)
			}
			access, err := a.Access(context.Background(), firstID)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []protocol.AdapterCapabilities{list.Sessions[0].Access.Capabilities, page.Access.Capabilities, access.Capabilities} {
				if !reflect.DeepEqual(value, a.Capabilities()) {
					t.Fatalf("source/session capability mismatch: %+v", value)
				}
			}
			if daemonCalls.Load() != 0 {
				t.Fatal("history or access attempted native metadata connection")
			}
		})
	}
}

func TestConfiguredMetadataFailureRetainsHistoryWithoutClaimingOnline(t *testing.T) {
	var daemonCalls atomic.Int32
	history := &historyFixture{}
	a, err := New(Options{History: history,
		Scope:      func(context.Context) (string, error) { return "verified-scope", nil },
		SocketPath: filepath.Join(t.TempDir(), "missing.sock"), UserID: "fixture-user",
		VerifyDaemon: func(context.Context, codex.LocalDaemonIdentity) error {
			daemonCalls.Add(1)
			return errors.New("untrusted daemon")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	status := a.Status(context.Background())
	if status.State != "disconnected" || status.Reason != "native_connection_unavailable" || status.DaemonState != "unavailable" {
		t.Fatalf("configured endpoint became online: %+v", status)
	}
	assertMetadataReadCapabilities(t, status.Capabilities, true)
	if _, err := a.Models(context.Background()); err == nil {
		t.Fatal("unavailable daemon supplied models")
	}
	if _, err := a.Usage(context.Background()); err == nil {
		t.Fatal("unavailable daemon supplied usage")
	}
	if history.calls.Load() != 0 || daemonCalls.Load() != 0 {
		t.Fatal("metadata failure fell back to history or admitted missing peer")
	}
	if _, err := a.Read(context.Background(), firstID, "", false, nil); err != nil {
		t.Fatal("metadata failure disabled history", err)
	}
}

func TestMetadataReadsPreserveScopeCancellationBeforeConnection(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, ErrUnavailable} {
		t.Run(cause.Error(), func(t *testing.T) {
			var daemonCalls atomic.Int32
			history := &historyFixture{}
			a, err := New(Options{History: history,
				Scope:      func(context.Context) (string, error) { return "", cause },
				SocketPath: filepath.Join(t.TempDir(), "missing.sock"), UserID: "fixture-user",
				VerifyDaemon: func(context.Context, codex.LocalDaemonIdentity) error {
					daemonCalls.Add(1)
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if _, err := a.Models(context.Background()); !errors.Is(err, cause) {
				t.Fatal("model read lost scope failure", err)
			}
			if _, err := a.Usage(context.Background()); !errors.Is(err, cause) {
				t.Fatal("usage read lost scope failure", err)
			}
			status := a.Status(context.Background())
			if status.State != "unavailable" || status.Capabilities.History || status.Capabilities.Models || status.Capabilities.Usage {
				t.Fatalf("unverified native scope retained capabilities: %+v", status)
			}
			if daemonCalls.Load() != 0 || history.calls.Load() != 0 {
				t.Fatal("failed scope reached native state")
			}
		})
	}
}

func assertMetadataReadCapabilities(t *testing.T, caps protocol.AdapterCapabilities, metadata bool) {
	t.Helper()
	if !caps.History || !caps.LazyMessages || caps.HistoryMode != "bounded" || caps.Models != metadata || caps.Usage != metadata || caps.Settings == nil {
		t.Fatalf("wrong read capabilities: %+v", caps)
	}
	if caps.Create || caps.Live || caps.IdleSend || caps.ActiveSend != "unsupported" || caps.Stop || len(caps.Settings) != 0 || caps.Rename || caps.Archive || caps.Uploads {
		t.Fatalf("metadata enabled native control: %+v", caps)
	}
}
