package claude

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func mutationFixture(t *testing.T) (*Adapter, string, context.Context) {
	t.Helper()
	a, nonce, ctx := stopFixture(t)
	yes := true
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
		Events: []modEvent{{Kind: "snapshot", CanStop: &yes, CanInput: &yes, CanSettings: &yes, ModelOptions: []string{"sonnet"}}}})
	if status != http.StatusOK {
		t.Fatal("mutation snapshot refused")
	}
	return a, nonce, ctx
}

func mutationCommand(t *testing.T, a *Adapter, nonce string) modCommand {
	t.Helper()
	pendingStop(t, a)
	status, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusOK || len(response.Commands) != 1 {
		t.Fatal("mutation not delivered")
	}
	a.mu.Lock()
	command := a.registrations[fixtureSession].pending.command
	a.mu.Unlock()
	_, repeated := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if len(repeated.Commands) != 0 {
		t.Fatal("mutation delivered twice")
	}
	return command
}

func mutationResult(t *testing.T, a *Adapter, nonce string, command modCommand) {
	t.Helper()
	status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: command.ID, OK: true}})
	if status != http.StatusOK {
		t.Fatal("native result refused")
	}
}

func TestModInputNeedsAcceptedCallAndNewMatchingObservation(t *testing.T) {
	for _, observationFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "observation-first", false: "result-first"}[observationFirst], func(t *testing.T) {
			a, nonce, ctx := mutationFixture(t)
			done := make(chan protocol.SendReceipt, 1)
			go func() {
				receipt, err := a.Send(ctx, fixtureSession, "exact content", "client-input")
				if err != nil {
					receipt.State = "error"
				}
				done <- receipt
			}()
			command := mutationCommand(t, a, nonce)
			observe := func(text string, sequence uint64) {
				status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: sequence,
					Events: []modEvent{{Kind: "start", TurnID: "new-turn", Text: protocol.Text(text), ObservedCommand: command.ID}}})
				if status != http.StatusOK {
					t.Fatal("observation refused")
				}
			}
			observe("prefix exact content suffix", 3)
			a.mu.Lock()
			matched := a.registrations[fixtureSession].inputs[0].Matched
			a.mu.Unlock()
			if matched {
				t.Fatal("substring confirmed input")
			}
			if observationFirst {
				observe("exact content", 4)
				select {
				case <-done:
					t.Fatal("observation alone accepted input")
				default:
				}
				mutationResult(t, a, nonce, command)
			} else {
				mutationResult(t, a, nonce, command)
				receipt := <-done
				if receipt.Kind != "submitted_observed" || receipt.State != "submitted" {
					t.Fatalf("wrong submitted receipt: %+v", receipt)
				}
				observe("exact content", 4)
			}
			if observationFirst {
				receipt := <-done
				if receipt.State != "observed" || receipt.TurnID != "new-turn" {
					t.Fatalf("missing observed receipt: %+v", receipt)
				}
			}
			page, err := a.Read(ctx, fixtureSession, "", false, nil)
			if err != nil || len(page.InputReceipts) != 1 || page.InputReceipts[0].State != "observed" {
				t.Fatalf("missing retained receipt: %+v %v", page.InputReceipts, err)
			}
		})
	}
}

func TestModUnknownRuntimeSubmitsWithoutInventingIdle(t *testing.T) {
	a, nonce, ctx := mutationFixture(t)
	a.mu.Lock()
	r := a.registrations[fixtureSession]
	r.status, r.activeTurn = "unknown", ""
	a.mu.Unlock()
	done := make(chan protocol.SendReceipt, 1)
	failed := make(chan error, 1)
	go func() {
		receipt, err := a.Send(ctx, fixtureSession, "native queued prompt", "unknown-state-client")
		if err != nil {
			failed <- err
			return
		}
		done <- receipt
	}()
	deadline := time.Now().Add(time.Second)
	for {
		select {
		case err := <-failed:
			t.Fatalf("unknown native runtime refused supported input: %v", err)
		default:
		}
		a.mu.Lock()
		queued := a.registrations[fixtureSession].pending != nil
		a.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native submission was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	command := mutationCommand(t, a, nonce)
	if command.Operation != "send" || command.TurnID != "" {
		t.Fatalf("unknown state invented a native turn: %+v", command)
	}
	mutationResult(t, a, nonce, command)
	receipt := <-done
	page, err := a.Read(ctx, fixtureSession, "", false, nil)
	if err != nil || receipt.State != "submitted" || receipt.TurnID != "" ||
		page.Runtime.Status != "unknown" || page.Runtime.ActiveTurnID != nil {
		t.Fatalf("submission fabricated native idle or acceptance: %+v %+v %v", receipt, page.Runtime, err)
	}
	if _, err := a.Send(ctx, fixtureSession, "do not resend", "new-client"); err == nil {
		t.Fatal("unknown-state submitted input allowed another attempt")
	}
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 3,
		Events: []modEvent{{Kind: "start", TurnID: "native-next-turn", Text: "native queued prompt", ObservedCommand: command.ID}}})
	page, err = a.Read(ctx, fixtureSession, "", false, nil)
	if status != http.StatusOK || err != nil || page.InputReceipts[0].State != "observed" ||
		page.InputReceipts[0].TurnID != "native-next-turn" || page.Runtime.Status != "active" {
		t.Fatalf("native turn did not resolve submission: %+v %v", page, err)
	}
}

func TestModUnknownInputBlocksNewClientAndNeverReplays(t *testing.T) {
	a, nonce, ctx := mutationFixture(t)
	cancelled, cancel := context.WithCancel(ctx)
	done := make(chan protocol.SendReceipt, 1)
	go func() {
		receipt, _ := a.Send(cancelled, fixtureSession, "unknown content", "unknown-client")
		done <- receipt
	}()
	_ = mutationCommand(t, a, nonce)
	cancel()
	if receipt := <-done; receipt.State != "unconfirmed" {
		t.Fatalf("unknown was promoted: %+v", receipt)
	}
	if _, err := a.Send(ctx, fixtureSession, "another content", "different-client"); err == nil {
		t.Fatal("unknown input allowed resend")
	}
	_, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if len(response.Commands) != 0 {
		t.Fatal("unknown mutation replayed")
	}
}

func TestModSettingsWaitForNextMatchingRequestAndReportActual(t *testing.T) {
	for _, actual := range []string{"low", "max"} {
		t.Run(actual, func(t *testing.T) {
			a, nonce, ctx := mutationFixture(t)
			done := make(chan error, 1)
			go func() {
				done <- a.UpdateSettings(ctx, fixtureSession, protocol.SessionSettings{Model: protocol.Known("sonnet"), Effort: protocol.Known("low")})
			}()
			command := mutationCommand(t, a, nonce)
			mutationResult(t, a, nonce, command)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			page, _ := a.Read(ctx, fixtureSession, "", false, nil)
			if page.Runtime.SettingsChange.State != "pending" {
				t.Fatal("ACK confirmed settings")
			}
			model := "sonnet"
			status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 3,
				Events: []modEvent{{Kind: "request", TurnID: "wrong-turn", Model: &model, Effort: &actual, ObservedCommand: command.ID}}})
			page, _ = a.Read(ctx, fixtureSession, "", false, nil)
			if status != http.StatusOK || page.Runtime.SettingsChange.State != "pending" {
				t.Fatal("stale request confirmed settings")
			}
			status, _ = bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 4,
				Events: []modEvent{{Kind: "start", TurnID: "next-turn"}, {Kind: "request", TurnID: "next-turn", Model: &model, Effort: &actual, ObservedCommand: command.ID}}})
			if status != http.StatusOK {
				t.Fatal("request evidence refused")
			}
			page, _ = a.Read(ctx, fixtureSession, "", false, nil)
			expected := "confirmed"
			if actual != "low" {
				expected = "failed"
			}
			change := page.Runtime.SettingsChange
			if change.State != expected || change.ActualEffort != actual || change.ActualModel != model || change.TurnID != "next-turn" {
				t.Fatalf("wrong applied evidence: %+v", change)
			}
		})
	}
}

func TestModNativeRefusalIsBoundedAndNeverObserved(t *testing.T) {
	for _, test := range []struct {
		refusal string
		want    string
	}{
		{"append_read_only", "Claude native append refused: main is a conversation no plugin may shape"},
		{"append_unbound", "Claude native append refused: this session keeps no conversation to append to"},
		{"append_identity", "Claude native append refused: a uuid the engine did not mint"},
		{"append_shape", "Claude native append refused the user-note shape"},
		{"private arbitrary text", "Claude native input refused"},
	} {
		t.Run(test.refusal, func(t *testing.T) {
			a, nonce, ctx := mutationFixture(t)
			done := make(chan error, 1)
			go func() {
				_, err := a.Send(ctx, fixtureSession, "unique refused input", "refused-client")
				done <- err
			}()
			command := mutationCommand(t, a, nonce)
			status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
				Result: &modCommandResult{ID: command.ID, OK: false, Refusal: test.refusal}})
			if status != http.StatusOK {
				t.Fatal("native refusal result rejected")
			}
			if err := <-done; err == nil || err.Error() != test.want {
				t.Fatalf("wrong native refusal: %v", err)
			}
			page, err := a.Read(ctx, fixtureSession, "", false, nil)
			if err != nil || len(page.InputReceipts) != 1 || page.InputReceipts[0].State != "rejected" ||
				page.InputReceipts[0].MessageID != "" {
				t.Fatalf("refusal fabricated acceptance: %+v %v", page.InputReceipts, err)
			}
			_, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
			if len(response.Commands) != 0 {
				t.Fatal("refused native command replayed")
			}
		})
	}
}

func TestModSubmittedObservationTimeoutStaysUnknown(t *testing.T) {
	a, nonce, ctx := mutationFixture(t)
	done := make(chan protocol.SendReceipt, 1)
	go func() { receipt, _ := a.Send(ctx, fixtureSession, "never observed", "timeout-client"); done <- receipt }()
	command := mutationCommand(t, a, nonce)
	mutationResult(t, a, nonce, command)
	<-done
	a.mu.Lock()
	a.registrations[fixtureSession].inputs[0].Deadline = time.Time{}
	a.mu.Unlock()
	page, _ := a.Read(ctx, fixtureSession, "", false, nil)
	if page.InputReceipts[0].State != "unconfirmed" {
		t.Fatal("observation timeout did not stay unknown")
	}
}
