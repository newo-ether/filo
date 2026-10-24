package service

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// This opt-in qualification uses a separately compiled current runner and the
// installed guardian. Only a dedicated tool-free native session is mutated.
// The retained private directory makes an uncertain outcome inspectable; neither
// timeout nor test cleanup kills accepted work or retries its input.
func TestCLIControlledNativeRunnerProbe(t *testing.T) {
	if os.Getenv("FILO_CLI_NATIVE_PROBE") != "1" {
		t.Skip("explicit native CLI probe is disabled")
	}
	runner := os.Getenv("FILO_CLI_PROBE_RUNNER")
	if !filepath.IsAbs(runner) {
		t.Fatal("native probe needs the absolute separately compiled runner")
	}
	if _, err := resolveCLIRuntime(); err != nil {
		t.Fatal(err)
	}
	guardian, err := deploymentPath("tools", guardianExecutableName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(guardian); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	nativeHistory := history.NewNativeHistory(func(context.Context) (string, error) { return codexHome, nil })
	scope := func(context.Context) (string, error) { return "dedicated-native-cli-probe", nil }
	reader, err := codexcli.New(codexcli.Options{History: nativeHistory, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	root, err := os.MkdirTemp("", "filo-cli-native-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained probe directory=%s", root)
	launch := func(directory string) error {
		command := exec.Command(runner, executorRunnerArgument, directory)
		command.Dir = root
		command.SysProcAttr = detachedProcessAttributes()
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			return err
		}
		return command.Process.Release()
	}
	options := CLIControlledOptions{Directory: root, Workspace: root, Reader: reader,
		History: nativeHistory, Scope: scope, Launch: launch, Readiness: 45 * time.Second}
	surface, err := NewCLIControlledSessions(options)
	if err != nil {
		t.Fatal(err)
	}
	defer surface.Close()
	ctx := context.Background()
	if status := surface.Status(ctx); status.State != "disconnected" {
		t.Fatalf("fresh source status=%+v", status)
	}
	clientID, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	created, receipt, err := surface.Create(ctx,
		"Reply exactly FILO_CLI_NATIVE_PROBE_OK. Do not use tools.", clientID, protocol.SessionSettings{})
	if err != nil {
		t.Fatalf("dedicated native create failed, input not retried: %v", err)
	}
	if receipt.TurnID == "" || receipt.ClientID != clientID {
		t.Fatal("native turn receipt is missing")
	}
	t.Logf("native accepted session=%s turn=%s client=%s", created.ID, receipt.TurnID, receipt.ClientID)
	if status := surface.Status(ctx); status.State != "connected" {
		t.Fatalf("accepted executor lacks verified readiness: %+v", status)
	}
	surface.Close()
	page := awaitCLINativeTerminal(t, nativeHistory, created.ID, receipt)
	if !cliProbeAssistant(page, receipt.TurnID, "FILO_CLI_NATIVE_PROBE_OK") {
		t.Fatal("exact native terminal did not contain expected assistant result")
	}
	t.Log("exact native input receipt and assistant completion observed after adapter Close")
	waitForCondition(t, 150*time.Second, "all dedicated native executors to retire", func() bool {
		entries, err := os.ReadDir(filepath.Join(root, "codex-cli-tasks", "executors"))
		if err != nil || len(entries) == 0 {
			return false
		}
		for _, entry := range entries {
			state, ok := readExecutorStateView(t, filepath.Join(root, "codex-cli-tasks", "executors", entry.Name()))
			if !ok || (state.Phase != "retired" && state.Phase != "exited") {
				return false
			}
		}
		return true
	})
	resumed, err := NewCLIControlledSessions(options)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	access, err := resumed.Access(ctx, created.ID)
	if err != nil || access.Capabilities.Live || !access.Capabilities.IdleSend {
		t.Fatalf("post-retirement idle access=%+v err=%v", access, err)
	}
	secondClient, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := resumed.Send(protocol.WithAccessBinding(ctx, access.Binding), created.ID,
		"Reply exactly FILO_CLI_NATIVE_RESEND_OK. Do not use tools.", secondClient)
	if err != nil {
		t.Fatalf("post-retirement resend failed, input not retried: %v", err)
	}
	if second.TurnID == "" || second.TurnID == receipt.TurnID || second.ClientID != secondClient {
		t.Fatal("resend did not return a distinct exact native turn")
	}
	resumed.Close()
	page = awaitCLINativeTerminal(t, nativeHistory, created.ID, second)
	if !cliProbeAssistant(page, second.TurnID, "FILO_CLI_NATIVE_RESEND_OK") {
		t.Fatal("resend exact terminal did not contain expected assistant result")
	}
	t.Logf("all-idle-retired resend completed session=%s turn=%s client=%s", created.ID, second.TurnID, second.ClientID)
}

func awaitCLINativeTerminal(t *testing.T, nativeHistory TaskSessionsHistory, id string, receipt protocol.SendReceipt) protocol.ConversationPage {
	t.Helper()
	var page protocol.ConversationPage
	waitForCondition(t, 120*time.Second, "exact persisted native receipt and terminal", func() bool {
		value, err := nativeHistory.Read(context.Background(), id, "", false, nil)
		if err != nil || value.Runtime == nil || !value.Runtime.CompletedTurnID.Known ||
			value.Runtime.CompletedTurnID.Value == nil || *value.Runtime.CompletedTurnID.Value != receipt.TurnID ||
			value.Runtime.ActiveTurnID != nil {
			return false
		}
		for _, input := range value.InputReceipts {
			if input.ClientID == receipt.ClientID && input.TurnID == receipt.TurnID && input.MessageID != "" {
				page = value
				return true
			}
		}
		return false
	})
	return page
}

func cliProbeAssistant(page protocol.ConversationPage, turnID, marker string) bool {
	for _, message := range page.Messages {
		if message.Role == "assistant" && message.TurnID == turnID && strings.Contains(string(message.Text), marker) {
			return true
		}
	}
	return false
}
