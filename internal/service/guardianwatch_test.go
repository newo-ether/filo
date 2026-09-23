package service

import (
	"errors"
	"testing"
)

type guardianTestProcess struct {
	live     bool
	released int
}

func (p *guardianTestProcess) alive() bool    { return p.live }
func (p *guardianTestProcess) close()         {}
func (p *guardianTestProcess) release() error { p.released++; p.live = false; return nil }

func TestGuardianPreservesNativeUntilConfirmedTerminal(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, kind := range []string{"active", "unknown", "systemError", "idle", "notLoaded"} {
		t.Run(kind, func(t *testing.T) {
			native := &guardianTestProcess{live: true}
			g := &guardianWatch{directory: t.TempDir(), state: map[string]any{}, task: id,
				keeper: &guardianTestProcess{}, native: native}
			done, err := g.step(func(method string, params any) (map[string]any, error) {
				if method != "thread/read" {
					t.Fatalf("unexpected mutation %s", method)
				}
				return map[string]any{"thread": map[string]any{"id": id, "status": map[string]any{"type": kind}}}, nil
			})
			want := kind == "idle" || kind == "notLoaded"
			if err != nil || done != want || (native.released == 1) != want {
				t.Fatalf("done=%v released=%d error=%v", done, native.released, err)
			}
		})
	}
}

func TestGuardianDoesNotReleaseForReadFailureOrWrongIdentity(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, failure := range []bool{false, true} {
		native := &guardianTestProcess{live: true}
		g := &guardianWatch{directory: t.TempDir(), state: map[string]any{}, task: id,
			keeper: &guardianTestProcess{}, native: native}
		_, err := g.step(func(string, any) (map[string]any, error) {
			if failure {
				return nil, errors.New("lost")
			}
			return map[string]any{"thread": map[string]any{"id": "wrong", "status": map[string]any{"type": "idle"}}}, nil
		})
		if err == nil || native.released != 0 {
			t.Fatal("unverified native release")
		}
	}
}

func TestGuardianLostBindingDoesNotChooseAmongLoadedTasks(t *testing.T) {
	native := &guardianTestProcess{live: true}
	g := &guardianWatch{directory: t.TempDir(), state: map[string]any{},
		keeper: &guardianTestProcess{}, native: native}
	_, err := g.step(func(method string, params any) (map[string]any, error) {
		if method != "thread/loaded/list" {
			t.Fatal("ambiguous task resumed")
		}
		return map[string]any{"data": []any{"one", "two"}}, nil
	})
	if err == nil || native.released != 0 {
		t.Fatal("ambiguous native scope released")
	}
}

func TestGuardianLiveKeeperAndNotificationFence(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	native := &guardianTestProcess{live: true}
	g := &guardianWatch{directory: t.TempDir(), state: map[string]any{}, task: id,
		keeper: &guardianTestProcess{live: true}, native: native}
	done, err := g.step(func(string, any) (map[string]any, error) { t.Fatal("keeper still owns native"); return nil, nil })
	if err != nil || done || native.released != 0 {
		t.Fatal("released a live keeper's native")
	}
	thread := map[string]any{"status": map[string]any{"type": "systemError"}}
	notify := func(task, method string) {
		g.notification(map[string]any{"method": method, "params": map[string]any{
			"threadId": task, "turn": map[string]any{"status": "failed", "id": "turn"}}})
	}
	notify("another", "turn/completed")
	if g.idle(thread) {
		t.Fatal("another task authorized release")
	}
	notify(id, "turn/completed")
	if !g.idle(thread) {
		t.Fatal("confirmed terminal failure did not settle")
	}
	notify(id, "turn/started")
	if g.idle(thread) {
		t.Fatal("terminal state leaked into a new turn")
	}
}
