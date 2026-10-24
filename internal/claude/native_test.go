package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

type nativeFixtureInput struct {
	writes chan map[string]json.RawMessage
}

func (w *nativeFixtureInput) Write(data []byte) (int, error) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return 0, err
	}
	w.writes <- message
	return len(data), nil
}
func (w *nativeFixtureInput) Close() error { return nil }

func nativeFixture(t *testing.T) (*NativeSession, *nativeFixtureInput) {
	t.Helper()
	input := &nativeFixtureInput{writes: make(chan map[string]json.RawMessage, 16)}
	s := &NativeSession{input: input, options: NativeOptions{Scope: "fixture"}, id: "e8a6fd60-58e0-4611-aa11-3210ab03cf84", binding: "fixture-binding",
		inputs: map[string]*nativeInput{}, controls: map[string]*nativeControl{}, updated: time.Now()}
	return s, input
}

func nativeFixtureEvent(t *testing.T, s *NativeSession, event any) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if err = s.handle(object); err != nil {
		t.Fatal(err)
	}
}

func nativeFixtureEcho(t *testing.T, s *NativeSession, text string) {
	nativeFixtureEvent(t, s, map[string]any{"type": "user", "session_id": s.id, "uuid": "74b81ec7-2f37-42ba-af24-17248d5670af", "isReplay": true, "message": map[string]any{"role": "user", "content": text}})
}
func nativeFixtureResult(t *testing.T, s *NativeSession) {
	nativeFixtureEvent(t, s, map[string]any{"type": "result", "subtype": "error_during_execution", "session_id": s.id, "uuid": "7072cc96-c39c-48c4-9355-c59d1ec11546", "terminal_reason": "aborted_streaming"})
}

func TestNativeAcceptanceSurvivesCanceledObserver(t *testing.T) {
	s, w := nativeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Send(ctx, "hello", "client"); done <- err }()
	<-w.writes
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	nativeFixtureEcho(t, s, "hello")
	receipt, err := s.Send(context.Background(), "hello", "client")
	if err != nil || !strings.HasPrefix(receipt.TurnID, ExecutionTurnPrefix) {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if len(w.writes) != 0 {
		t.Fatal("uncertain input was replayed")
	}
	page := s.Read()
	if len(page.InputReceipts) != 1 || page.InputReceipts[0].MessageID != "74b81ec7-2f37-42ba-af24-17248d5670af" || page.Messages[0].NativeID != page.InputReceipts[0].MessageID {
		t.Fatal("native acceptance identity missing")
	}
	if _, err := s.Send(context.Background(), "different", "client"); err == nil {
		t.Fatal("client identity reused with changed input")
	}
	if _, err := s.Send(context.Background(), "another", "other"); err == nil {
		t.Fatal("active input queued")
	}
}

func TestNativeReceiptRejectsForeignAndSyntheticMessages(t *testing.T) {
	s, w := nativeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Send(ctx, "hello", "client")
	<-w.writes
	nativeFixtureEvent(t, s, map[string]any{"type": "user", "session_id": s.id, "uuid": "9db5bc98-3858-4f3c-be43-7959b3935816", "message": map[string]any{"role": "user", "content": "interrupt notification"}})
	if len(s.Read().InputReceipts) != 0 {
		t.Fatal("synthetic native user notification accepted as input")
	}
	data, _ := json.Marshal(map[string]any{"type": "user", "session_id": "2510bdc8-f214-4d0e-9ce0-d02c9f93b222", "uuid": "9db5bc98-3858-4f3c-be43-7959b3935816", "isReplay": true, "message": map[string]any{"role": "user", "content": "hello"}})
	var event map[string]json.RawMessage
	json.Unmarshal(data, &event)
	if err := s.handle(event); err == nil {
		t.Fatal("foreign session input accepted")
	}
}

func TestNativeStopRequiresAckAndTerminalEitherOrder(t *testing.T) {
	for _, terminalFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "terminal-first", false: "ack-first"}[terminalFirst], func(t *testing.T) {
			s, w := nativeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sent := make(chan error, 1)
			go func() { _, err := s.Send(ctx, "hello", "client"); sent <- err }()
			<-w.writes
			nativeFixtureEcho(t, s, "hello")
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			turn := s.Read().InputReceipts[0].TurnID
			stopped := make(chan error, 1)
			go func() { stopped <- s.Stop(ctx, turn) }()
			request := <-w.writes
			ack := func() {
				nativeFixtureEvent(t, s, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": stringField(request, "request_id"), "response": map[string]any{}}})
			}
			if terminalFirst {
				nativeFixtureResult(t, s)
			} else {
				ack()
			}
			select {
			case err := <-stopped:
				t.Fatalf("Stop succeeded with one evidence only: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
			if terminalFirst {
				ack()
			} else {
				nativeFixtureResult(t, s)
			}
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			page := s.Read()
			if page.Runtime.ActiveTurnID != nil || *page.Runtime.CompletedTurnID.Value != turn {
				t.Fatal("terminal execution not reconciled")
			}
		})
	}
}

func TestNativeStateAccessIdentityAndModelEvidence(t *testing.T) {
	s, w := nativeFixture(t)
	initial := s.Access()
	if !initial.Capabilities.IdleSend || initial.Capabilities.Stop || len(initial.Capabilities.Settings) != 2 || initial.CanonicalSessionRef == initial.Binding {
		t.Fatal("invalid idle access identity or capabilities")
	}
	s.binding = "another-binding"
	if s.Access().CanonicalSessionRef != initial.CanonicalSessionRef {
		t.Fatal("canonical identity aliases control binding")
	}
	s.models = []protocol.Model{{ID: "sonnet", Name: "Sonnet"}}
	s.model = "previous-native-model"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.SetModel(ctx, "sonnet") }()
	request := <-w.writes
	nativeFixtureEvent(t, s, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": stringField(request, "request_id"), "response": map[string]any{}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Read().Runtime.Model != nil {
		t.Fatal("model ACK was advertised as actual runtime readback")
	}
	nativeFixtureEvent(t, s, map[string]any{"type": "system", "subtype": "init", "session_id": s.id, "model": "claude-sonnet-native-resolved"})
	if model := s.Read().Runtime.Model; model == nil || *model != "claude-sonnet-native-resolved" {
		t.Fatal("native resolved model not projected")
	}
	go func() { _, err := s.Send(ctx, "hello", "client"); done <- err }()
	<-w.writes
	if caps := s.Access().Capabilities; caps.IdleSend || caps.Stop || len(caps.Settings) != 0 {
		t.Fatal("pending admission capabilities not narrowed")
	}
	nativeFixtureEcho(t, s, "hello")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if page := s.Read(); page.Runtime.Status != "active" || !page.Access.Capabilities.Stop || page.Access.Capabilities.IdleSend {
		t.Fatal("active runtime or access contract invalid")
	}
	nativeFixtureResult(t, s)
	if caps := s.Access().Capabilities; !caps.IdleSend || caps.Stop || len(caps.Settings) != 2 {
		t.Fatal("terminal access did not return to idle")
	}
}

func nativeFixtureAck(t *testing.T, s *NativeSession, request map[string]json.RawMessage, response any) {
	nativeFixtureEvent(t, s, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": stringField(request, "request_id"), "response": response}})
}

func nativeFixtureSubtype(request map[string]json.RawMessage) string {
	var body map[string]json.RawMessage
	_ = json.Unmarshal(request["request"], &body)
	return stringField(body, "subtype")
}

func TestNativeEffortRequiresAppliedReadback(t *testing.T) {
	for _, applied := range []string{"high", "max"} {
		t.Run(applied, func(t *testing.T) {
			s, w := nativeFixture(t)
			s.models = []protocol.Model{{ID: "opus", Name: "Opus", ReasoningEfforts: []string{"low", "high", "max"}}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.SetEffort(ctx, "turbo"); err == nil {
				t.Fatal("unknown effort admitted")
			}
			done := make(chan error, 1)
			go func() { done <- s.SetEffort(ctx, "high") }()
			request := <-w.writes
			if nativeFixtureSubtype(request) != "apply_flag_settings" {
				t.Fatalf("first control %s", nativeFixtureSubtype(request))
			}
			nativeFixtureAck(t, s, request, map[string]any{})
			request = <-w.writes
			if nativeFixtureSubtype(request) != "get_settings" {
				t.Fatalf("readback control %s", nativeFixtureSubtype(request))
			}
			nativeFixtureAck(t, s, request, map[string]any{"applied": map[string]any{"effort": applied}})
			err := <-done
			effort := s.Read().Runtime.Effort
			if applied == "high" && (err != nil || !effort.Known || *effort.Value != "high") {
				t.Fatalf("confirmed effort err=%v runtime=%+v", err, effort)
			}
			if applied == "max" && (err == nil || effort.Known) {
				t.Fatalf("overridden effort reported success: err=%v runtime=%+v", err, effort)
			}
		})
	}
}

func TestNativeRenameUsesOfficialControl(t *testing.T) {
	s, w := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Rename(ctx, " \n"); err == nil {
		t.Fatal("blank title admitted")
	}
	if !s.Access().Capabilities.Rename {
		t.Fatal("live idle session cannot rename")
	}
	done := make(chan error, 1)
	go func() { done <- s.Rename(ctx, "New title") }()
	request := <-w.writes
	var body map[string]json.RawMessage
	_ = json.Unmarshal(request["request"], &body)
	if stringField(body, "subtype") != "rename_session" || stringField(body, "title") != "New title" {
		t.Fatalf("rename control %s", request["request"])
	}
	if s.Session().Title != "Claude Code (Filo-owned)" {
		t.Fatal("title changed before native acknowledgement")
	}
	nativeFixtureAck(t, s, request, map[string]any{})
	if err := <-done; err != nil || s.Session().Title != "New title" {
		t.Fatalf("rename err=%v title=%q", err, s.Session().Title)
	}
}

var _ io.WriteCloser = (*nativeFixtureInput)(nil)
