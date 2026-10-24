//go:build windows

package service

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// The opt-in probe admits only an exact, separately launched ordinary terminal
// fixture. It never starts a daemon, resumes history or writes terminal input.
func TestCLINativeDaemonQualification(t *testing.T) {
	if os.Getenv("FILO_CLI_DAEMON_PROBE") != "1" {
		t.Skip("explicit ordinary native daemon probe is disabled")
	}
	cwd := os.Getenv("FILO_CLI_DAEMON_PROBE_CWD")
	id := os.Getenv("FILO_CLI_DAEMON_PROBE_THREAD")
	if !filepath.IsAbs(cwd) || !uuidPattern.MatchString(id) {
		t.Fatal("exact self-created fixture cwd and thread are required")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(account.HomeDir, ".codex")
	homeReader := func(context.Context) (string, error) { return home, nil }
	connect := existingCLIDaemon("", account.Uid, homeReader, func(_ context.Context, identity codex.LocalDaemonIdentity) error {
		if !strings.EqualFold(filepath.Clean(identity.CodexHome), filepath.Clean(home)) {
			return codexcli.ErrUnavailable
		}
		return nil
	})
	host, err := connect(context.Background())
	if err != nil {
		t.Fatalf("native connector: %v", err)
	}
	defer host.Close()
	ids, err := cliSharedLoaded(host)
	if err != nil || !containsTurn(ids, id) {
		t.Fatal("exact fixture thread is not loaded")
	}
	value, err := host.Rpc.Request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
	thread := sharedProbeObject(sharedProbeObject(value)["thread"])
	path, _ := thread["cwd"].(string)
	if err != nil || thread["id"] != id || thread["parentThreadId"] != nil ||
		!strings.EqualFold(filepath.Clean(path), filepath.Clean(cwd)) {
		t.Fatal("exact fixture identity/cwd admission failed")
	}
	t.Logf("same native daemon loaded fixture thread=%s", id)
	root := t.TempDir()
	nativeHistory := history.NewNativeHistory(homeReader)
	scope := func(context.Context) (string, error) { return "ordinary-daemon-probe", nil }
	metadata := newCLISourceMetadata(scope, func(_ context.Context, selected string) error {
		if !strings.EqualFold(filepath.Clean(selected), filepath.Clean(home)) {
			return codexcli.ErrUnavailable
		}
		return nil
	})
	defer metadata.Close()
	reader, err := codexcli.New(codexcli.Options{History: nativeHistory, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	surface, err := NewCLIControlledSessions(CLIControlledOptions{Directory: root, Workspace: cwd,
		Reader: reader, Metadata: metadata, History: nativeHistory, Scope: scope,
		NativeHome: homeReader, Daemon: connect})
	if err != nil {
		t.Fatal(err)
	}
	defer surface.Close()
	page, err := surface.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Sessions {
		found = found || row.ID == id
	}
	if !found {
		t.Fatal("production daemon catalog omitted fixture")
	}
	detach, err := surface.Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	awaitDaemonPage(t, surface, id, "initial native idle", func(v protocol.ConversationPage) bool {
		return v.Runtime != nil && v.Runtime.Status == "idle" && v.Runtime.ActiveTurnID == nil
	})
	models, err := surface.Models(context.Background())
	if err != nil || len(models) == 0 {
		t.Fatalf("native models: count=%d err=%v", len(models), err)
	}
	t.Logf("native model catalog count=%d", len(models))
	switch os.Getenv("FILO_CLI_DAEMON_PROBE_STAGE") {
	case "controls":
		qualifyDaemonControls(t, surface, host, id, models)
	case "http":
		qualifyDaemonHTTP(t, surface, scope, id)
	default:
		client, err := randomUUID()
		if err != nil {
			t.Fatal(err)
		}
		receipt := sharedSurfaceReceipt(t, surface, id,
			"Reply exactly FILO_NATIVE_DAEMON_OK. Do not use tools.", client)
		awaitDaemonReceipt(t, surface, id, receipt)
		awaitDaemonTerminal(t, surface, id, receipt.TurnID)
	}
}

func awaitDaemonPage(t *testing.T, surface *CLIControlledSessions, id, description string,
	accept func(protocol.ConversationPage) bool) protocol.ConversationPage {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		page, err := surface.Read(context.Background(), id, "", false, nil)
		if err != nil {
			t.Fatalf("%s read: %v", description, err)
		}
		if accept(page) {
			return page
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s absent; native mutation is never replayed", description)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func awaitDaemonReceipt(t *testing.T, surface *CLIControlledSessions, id string, receipt protocol.SendReceipt) {
	t.Helper()
	if receipt.TurnID == "" || receipt.ClientID == "" {
		t.Fatal("native turn/client receipt missing")
	}
	awaitDaemonPage(t, surface, id, "exact native user item", func(page protocol.ConversationPage) bool {
		for _, input := range page.InputReceipts {
			if input.ClientID == receipt.ClientID && input.TurnID == receipt.TurnID && input.MessageID != "" {
				t.Logf("exact native receipt client=%s turn=%s message=%s", input.ClientID, input.TurnID, input.MessageID)
				return true
			}
		}
		return false
	})
}

func awaitDaemonTerminal(t *testing.T, surface *CLIControlledSessions, id, turn string) {
	t.Helper()
	awaitDaemonPage(t, surface, id, "exact native terminal/idle", func(page protocol.ConversationPage) bool {
		runtime := page.Runtime
		return runtime != nil && runtime.Status == "idle" && runtime.ActiveTurnID == nil &&
			runtime.CompletedTurnID.Known && runtime.CompletedTurnID.Value != nil && *runtime.CompletedTurnID.Value == turn
	})
	t.Logf("same-turn terminal and idle turn=%s", turn)
}

func daemonMutationContext(t *testing.T, surface *CLIControlledSessions, id string) context.Context {
	t.Helper()
	access, err := surface.Access(context.Background(), id)
	if err != nil || !access.Capabilities.Live {
		t.Fatalf("fresh native access: %v", err)
	}
	return protocol.WithAccessBinding(context.Background(), access.Binding)
}

func qualifyDaemonControls(t *testing.T, surface *CLIControlledSessions, host *codex.CodexHost,
	id string, models []protocol.Model) {
	t.Helper()
	initial, err := surface.Read(context.Background(), id, "", false, nil)
	if err != nil || initial.Runtime == nil || initial.Runtime.Model == nil {
		t.Fatal("initial native model readback is missing")
	}
	original := *initial.Runtime.Model
	var selected protocol.Model
	for _, model := range models {
		if model.ID != original && len(model.ReasoningEfforts) > 0 {
			selected = model
			break
		}
	}
	if selected.ID == "" {
		t.Fatal("native catalog has no alternate model/effort")
	}
	effort := selected.ReasoningEfforts[0]
	patch := protocol.SessionSettings{Model: protocol.Known(selected.ID), Effort: protocol.Known(effort)}
	if err := surface.UpdateSettings(daemonMutationContext(t, surface, id), id, patch); err != nil {
		t.Fatalf("native settings apply: %v; not replayed", err)
	}
	awaitDaemonPage(t, surface, id, "applied native model and effort", func(page protocol.ConversationPage) bool {
		runtime := page.Runtime
		return runtime != nil && runtime.Model != nil && *runtime.Model == selected.ID &&
			runtime.Effort.Known && runtime.Effort.Value != nil && *runtime.Effort.Value == effort
	})
	t.Logf("fresh native applied settings model=%s effort=%s", selected.ID, effort)
	// Restore only a confirmed change before the tool-free execution probe.
	restore := protocol.SessionSettings{Model: protocol.Known(original)}
	if initial.Runtime.Effort.Known && initial.Runtime.Effort.Value != nil {
		restore.Effort = protocol.Known(*initial.Runtime.Effort.Value)
	}
	if err := surface.UpdateSettings(daemonMutationContext(t, surface, id), id, restore); err != nil {
		t.Fatalf("fixture settings restore: %v", err)
	}
	client, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	receipt := sharedSurfaceReceipt(t, surface, id,
		"Write integers 1 through 100000, one per line. Do not use tools.", client)
	awaitDaemonReceipt(t, surface, id, receipt)
	steerClient, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	steer, err := surface.Send(daemonMutationContext(t, surface, id), id,
		"Continue the numeric list with one number per line. Do not use tools.", steerClient)
	if err != nil || steer.TurnID != receipt.TurnID {
		t.Fatalf("native same-turn steer: turn=%s err=%v; not replayed", steer.TurnID, err)
	}
	awaitDaemonReceipt(t, surface, id, steer)
	if err := surface.Stop(daemonMutationContext(t, surface, id), id, receipt.TurnID); err != nil {
		t.Fatalf("exact-turn native stop: %v; not replayed", err)
	}
	awaitDaemonTerminal(t, surface, id, receipt.TurnID)
	value, err := host.Rpc.Request("thread/turns/list", map[string]any{
		"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "summary",
	})
	fields := sharedProbeObject(value)
	turns, _ := fields["data"].([]any)
	if err != nil || len(turns) != 1 || sharedProbeObject(turns[0])["id"] != receipt.TurnID ||
		sharedProbeObject(turns[0])["status"] != "interrupted" {
		t.Fatal("native Stop lacks exact interrupted readback")
	}
	t.Log("native same-turn steer and exact interrupted/idle readback passed")
}
