package claude

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func stopFixture(t *testing.T) (*Adapter, string, context.Context) {
	t.Helper()
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	canStop := true
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "snapshot", CanStop: &canStop}, {Kind: "start", TurnID: "native-turn"}}})
	if status != http.StatusOK {
		t.Fatal("native control fixture refused")
	}
	ctx, cancel := context.WithTimeout(protocol.WithAccessBinding(context.Background(), nonce), 2*time.Second)
	t.Cleanup(cancel)
	return a, nonce, ctx
}

func pendingStop(t *testing.T, a *Adapter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	for {
		a.mu.Lock()
		queued := a.registrations[fixtureSession].pending != nil
		a.mu.Unlock()
		if queued {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("native stop was not queued")
			return
		case <-time.After(time.Millisecond):
		}
	}
}

func deliverStop(t *testing.T, a *Adapter, nonce string) modCommand {
	t.Helper()
	pendingStop(t, a)
	status, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusOK || len(response.Commands) != 1 {
		t.Fatalf("control not delivered: %d %+v", status, response)
	}
	command := response.Commands[0].(map[string]any)
	return modCommand{ID: command["id"].(string), Operation: command["operation"].(string), TurnID: command["turnId"].(string)}
}

func TestModStopNeedsExactResultAndNativeTerminal(t *testing.T) {
	for _, terminalFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "result-first", true: "terminal-first"}[terminalFirst], func(t *testing.T) {
			a, nonce, ctx := stopFixture(t)
			done := make(chan error, 1)
			go func() { done <- a.Stop(ctx, fixtureSession, "native-turn") }()
			command := deliverStop(t, a, nonce)
			if command.Operation != "stop" || command.TurnID != "native-turn" {
				t.Fatal("wrong native control")
			}
			_, second := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
			if len(second.Commands) != 0 {
				t.Fatal("native operation replayed")
			}
			complete := func() {
				status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
					Events: []modEvent{{Kind: "complete", TurnID: "native-turn", Aborted: true}, {Kind: "start", TurnID: "next-turn"}}})
				if status != http.StatusOK {
					t.Fatal("terminal refused")
				}
			}
			result := func() {
				status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
					Result: &modCommandResult{ID: command.ID, OK: true}})
				if status != http.StatusOK {
					t.Fatal("exact result refused")
				}
			}
			if terminalFirst {
				complete()
			} else {
				result()
			}
			select {
			case err := <-done:
				t.Fatalf("incomplete evidence accepted: %v", err)
			default:
			}
			if terminalFirst {
				result()
			} else {
				complete()
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestModStopRejectsWrongOwnerTurnAndReceipt(t *testing.T) {
	a, nonce, ctx := stopFixture(t)
	for _, invalid := range []context.Context{context.Background(), protocol.WithAccessBinding(ctx, "stale")} {
		if err := a.Stop(invalid, fixtureSession, "native-turn"); err == nil {
			t.Fatal("unbound stop accepted")
		}
	}
	if err := a.Stop(ctx, fixtureSession, "other-turn"); err == nil {
		t.Fatal("wrong turn accepted")
	}
	done := make(chan error, 1)
	go func() { done <- a.Stop(ctx, fixtureSession, "native-turn") }()
	command := deliverStop(t, a, nonce)
	status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: "wrong-command", OK: true}})
	if status != http.StatusConflict {
		t.Fatal("wrong receipt accepted")
	}
	status, _ = bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: command.ID, OK: false}})
	if status != http.StatusOK || <-done == nil {
		t.Fatal("native refusal not preserved")
	}
}

func TestModStopScopeIsCheckedAgainBeforeDelivery(t *testing.T) {
	a, nonce, ctx := stopFixture(t)
	var valid atomic.Bool
	valid.Store(true)
	ctx = protocol.WithScopeValidation(ctx, func(context.Context) error {
		if !valid.Load() {
			return errors.New("source changed")
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- a.Stop(ctx, fixtureSession, "native-turn") }()
	pendingStop(t, a)
	valid.Store(false)
	_, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if len(response.Commands) != 0 {
		t.Fatal("changed source dispatched control")
	}
	bridgeCall(t, a, "revoke", envelope{SessionID: fixtureSession, Nonce: nonce})
	if err := <-done; err == nil {
		t.Fatal("revoked control reported success")
	}
}
