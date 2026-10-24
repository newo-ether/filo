package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

func TestClaudeExecutorReadsNeverStartBroker(t *testing.T) {
	t.Setenv(serviceHelperEnvironment, "console-exit")
	launches := 0
	s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: filepath.Join(t.TempDir(), "broker"), Workspace: t.TempDir(), NativeScopeID: "fixture", Executable: os.Args[0], Launch: func(string) error { launches++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Status().State == "connected" {
		t.Fatal("executable availability was treated as a native handshake")
	}
	if _, err = s.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Models(context.Background()); err == nil {
		t.Fatal("model read unexpectedly had a native catalog")
	}
	if launches != 0 {
		t.Fatal("read started a native owner")
	}
}

func TestClaudeExecutorBrokerAuthenticationAndScope(t *testing.T) {
	broker := &claudeBroker{config: claudeBrokerConfig{Token: strings.Repeat("a", 64)}, sessions: map[string]*claude.NativeSession{}, creating: map[string]*claudeCreation{}}
	server := httptest.NewServer(broker)
	defer server.Close()
	directory := t.TempDir()
	config := claudeBrokerConfig{Token: strings.Repeat("a", 64), Workspace: t.TempDir(), Scope: "fixture"}
	if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
		t.Fatal(err)
	}
	if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: server.URL, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: directory, Workspace: config.Workspace, NativeScopeID: config.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Status().State == "connected" {
		t.Fatal("broker without native session was considered connected")
	}
	if _, err := s.call(context.Background(), claudeBrokerRequest{Operation: "send", ID: "unowned", Text: "hello", ClientID: "test"}, false); err != nil {
		t.Fatal(err)
	}
	config.Token = strings.Repeat("b", 64)
	if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
		t.Fatal(err)
	}
	if _, err = s.call(context.Background(), claudeBrokerRequest{Operation: "list"}, false); err == nil {
		t.Fatal("wrong broker credential was accepted")
	}
}

// This child runs the production runner, separately from the test's gateway
// client. The native process and broker are real; only stdout diagnostics are hidden.
func TestClaudeExecutorBrokerProcess(t *testing.T) {
	if os.Getenv("FILO_CLAUDE_BROKER_CHILD") != "1" {
		t.Skip("private broker subprocess only")
	}
	if err := RunClaudeExecutorRunner([]string{os.Getenv("FILO_CLAUDE_BROKER_DIRECTORY")}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestClaudeExecutorNativeReopenAndStop(t *testing.T) {
	if os.Getenv("FILO_CLAUDE_BROKER_PROBE") != "1" {
		t.Skip("explicit native broker probe is disabled")
	}
	if _, err := claude.ResolveNativeExecutable(""); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var child *exec.Cmd
	options := ClaudeExecutorOptions{Directory: filepath.Join(root, "broker"), Workspace: root, NativeScopeID: "native-broker-probe"}
	options.Launch = func(directory string) error {
		child = exec.Command(os.Args[0], "-test.run=^TestClaudeExecutorBrokerProcess$", "-test.timeout=100s")
		child.Dir = root
		child.SysProcAttr = detachedProcessAttributes()
		child.Env = append(os.Environ(), "FILO_CLAUDE_BROKER_CHILD=1", "FILO_CLAUDE_BROKER_DIRECTORY="+directory)
		return child.Start()
	}
	s, err := NewClaudeExecutorSessions(options)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Cleanup is allowed only after this test observes the exact terminal. Never
	// kill a broker that might still hold an accepted generation.
	terminal := false
	defer func() {
		if child != nil && terminal {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	if s.Status().State == "connected" {
		t.Fatal("new executor appeared online without a handshake")
	}
	session, first, err := s.Create(ctx, "Reply exactly FILO_BROKER_FIRST_OK. Do not use any tools.", "first", protocol.SessionSettings{})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(client *ClaudeExecutorSessions, turn string) protocol.ConversationPage {
		t.Helper()
		for {
			page, err := client.Read(ctx, session.ID, "", false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if page.Runtime != nil && page.Runtime.CompletedTurnID.Known && page.Runtime.CompletedTurnID.Value != nil && *page.Runtime.CompletedTurnID.Value == turn {
				return page
			}
			select {
			case <-ctx.Done():
				t.Fatal("exact native terminal was not observed")
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	page := wait(s, first.TurnID)
	if len(page.InputReceipts) != 1 || page.InputReceipts[0].MessageID == "" || !strings.HasPrefix(first.TurnID, claude.ExecutionTurnPrefix) {
		t.Fatal("native user acceptance evidence missing")
	}
	if s.Status().State != "connected" {
		t.Fatal("handshaken broker not online")
	}
	models, err := s.Models(ctx)
	if err != nil || len(models) == 0 {
		t.Fatalf("models=%d err=%v", len(models), err)
	}
	if err := s.SetModel(ctx, session.ID, models[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := s.Send(ctx, session.ID, "Write five hundred numbered sentences. Do not use any tools.", "second")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := NewClaudeExecutorSessions(options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	page, err = reopened.Read(ctx, session.ID, "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.InputReceipts) != 2 || page.InputReceipts[1].TurnID != second.TurnID {
		t.Fatal("native acceptance was not retained across client lifetime")
	}
	if page.Runtime == nil || page.Runtime.Status != "active" || page.Runtime.Model == nil {
		t.Fatal("native init model readback or active runtime missing after model control")
	}
	t.Logf("actual model readback=%s", *page.Runtime.Model)
	if err := reopened.Stop(ctx, session.ID, second.TurnID); err != nil {
		t.Fatal(err)
	}
	page = wait(reopened, second.TurnID)
	terminal = true
	if page.Runtime.ActiveTurnID != nil {
		t.Fatal("Stop left an active execution")
	}
	data, _ := json.Marshal(page.InputReceipts)
	t.Logf("real broker reopened; receipts=%d exactTerminal=true modelAck=true stopAckAndTerminal=true boundedReceiptBytes=%d", len(page.InputReceipts), len(data))
}
