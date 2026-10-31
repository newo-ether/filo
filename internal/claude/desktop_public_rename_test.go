package claude

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

type publicRenameResult struct {
	session protocol.Session
	err     error
}

func startPublicRename(t *testing.T, a *Adapter, ctx context.Context) <-chan publicRenameResult {
	t.Helper()
	done := make(chan publicRenameResult, 1)
	go func() {
		session, err := a.Rename(ctx, fixtureSession, "new title")
		done <- publicRenameResult{session, err}
	}()
	pendingStop(t, a)
	return done
}

func awaitPublicRename(t *testing.T, done <-chan publicRenameResult) publicRenameResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("public rename did not finish")
		return publicRenameResult{}
	}
}

func publishRenameResult(t *testing.T, a *Adapter, nonce, id, status string) {
	t.Helper()
	value := &desktopMetadata{Status: status}
	if status == "applied" {
		value.SessionID, value.HostSessionID, value.Title = fixtureSession, fixtureHostSession, stringPointer("new title")
	}
	code, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: id, OK: status == "applied", Rename: value}})
	if code != http.StatusOK {
		t.Fatal("public native result rejected", code)
	}
}

func TestDesktopPublicRenamePublishesOnlyExactAppliedReadback(t *testing.T) {
	a, nonce := discoveryFixture(t)
	identity := desktopIdentityFixture(t)
	writeDesktopTitle := func(title string) {
		t.Helper()
		data := `{"sessionId":"` + fixtureHostSession + `","cliSessionId":"` + fixtureSession + `","title":"` + title + `"}`
		if err := os.WriteFile(identity.path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeDesktopTitle("old title")
	var changes atomic.Int32
	a.onChange = func(string) { changes.Add(1) }
	access, err := a.Access(context.Background(), fixtureSession)
	if err != nil || !access.Capabilities.Rename || !a.Descriptor().Capabilities.Rename || Capabilities().Rename {
		t.Fatal("Desktop source/session ceiling or terminal ceiling incorrect", access, err)
	}
	ctx := protocol.WithAccessBinding(context.Background(), nonce)
	done := startPublicRename(t, a, ctx)
	page, err := a.Read(ctx, fixtureSession, "", false, nil)
	if err != nil || page.Runtime.Title == nil || *page.Runtime.Title != "old title" || changes.Load() != 0 {
		t.Fatal("rename projected before applied native evidence", page, err)
	}
	id := pullRename(t, a, nonce, "new title")
	publishRenameResult(t, a, nonce, id, "applied")
	result := awaitPublicRename(t, done)
	if result.err != nil || result.session.ID != fixtureSession || result.session.Title != "new title" ||
		result.session.UpdatedAt <= 0 || changes.Load() != 2 || result.session.Access == nil || !result.session.Access.Capabilities.Rename {
		t.Fatal("applied title not published", result, changes.Load())
	}
	page, err = a.Read(ctx, fixtureSession, "", false, nil)
	if err != nil || page.Runtime.Title == nil || *page.Runtime.Title != "new title" || len(page.Messages) != 0 {
		t.Fatal("stale persisted title clobbered applied readback or rename created history rows", page, err)
	}
	writeDesktopTitle("new title")
	_, _ = a.Read(ctx, fixtureSession, "", false, nil)
	writeDesktopTitle("later native title")
	page, err = a.Read(ctx, fixtureSession, "", false, nil)
	if err != nil || page.Runtime.Title == nil || *page.Runtime.Title != "later native title" {
		t.Fatal("native store stopped refreshing after catching up", page, err)
	}
}

func TestDesktopPublicRenameMissingRegistrationMappingAndBindingRefuse(t *testing.T) {
	a, nonce := discoveryFixture(t)
	t.Setenv("LOCALAPPDATA", t.TempDir())
	ctx := protocol.WithAccessBinding(context.Background(), nonce)
	access, err := a.Access(ctx, fixtureSession)
	if err != nil || access.Capabilities.Rename {
		t.Fatal("missing mapping granted rename")
	}
	for _, bound := range []context.Context{context.Background(), protocol.WithAccessBinding(ctx, "stale"), ctx} {
		if _, err := a.Rename(bound, fixtureSession, "new title"); err == nil {
			t.Fatal("missing binding or mapping admitted rename")
		}
	}
	if a.registrations[fixtureSession].pending != nil {
		t.Fatal("missing mapping queued native mutation")
	}
	desktopIdentityFixture(t)
	delete(a.registrations, fixtureSession)
	if _, err := a.Rename(ctx, fixtureSession, "new title"); err == nil {
		t.Fatal("missing registration admitted rename")
	}
	terminal := fixtureAdapter(t)
	terminalNonce := registerFixture(t, terminal, fixtureSession)
	if _, err := terminal.Rename(protocol.WithAccessBinding(ctx, terminalNonce), fixtureSession, "new title"); err == nil {
		t.Fatal("terminal Mod gained rename")
	}
}

func TestDesktopPublicRenameUnknownRefusalAndScopeDoNotPublish(t *testing.T) {
	for _, scenario := range []string{"refused", "unknown", "cancel", "timeout", "scope", "rebind", "mapping"} {
		t.Run(scenario, func(t *testing.T) {
			a, nonce := discoveryFixture(t)
			identity := desktopIdentityFixture(t)
			var changes atomic.Int32
			a.onChange = func(string) { changes.Add(1) }
			ctx, cancel := context.WithCancel(protocol.WithAccessBinding(context.Background(), nonce))
			defer cancel()
			if scenario == "timeout" {
				var timeoutCancel context.CancelFunc
				ctx, timeoutCancel = context.WithTimeout(ctx, 150*time.Millisecond)
				defer timeoutCancel()
			}
			if scenario == "scope" {
				ctx = protocol.WithScopeValidation(ctx, func(context.Context) error {
					a.mu.Lock()
					defer a.mu.Unlock()
					if r := a.registrations[fixtureSession]; r != nil && r.pending != nil && r.pending.result != nil {
						return errors.New("scope changed after native result")
					}
					return nil
				})
			}
			done := startPublicRename(t, a, ctx)
			id := pullRename(t, a, nonce, "new title")
			switch scenario {
			case "cancel":
				cancel()
			case "timeout":
			case "rebind":
				a.mu.Lock()
				a.registrations[fixtureSession].nonce = "changed"
				notifyModCommand(a.registrations[fixtureSession])
				a.mu.Unlock()
			case "mapping":
				writeDesktopIdentityFixture(t, identity.path, fixtureHostSession, fixtureOther)
				cancel()
			default:
				state := scenario
				if scenario == "scope" {
					state = "applied"
				}
				publishRenameResult(t, a, nonce, id, state)
			}
			result := awaitPublicRename(t, done)
			if result.err == nil || result.session.Title != "" {
				t.Fatal("uncertain/refused rename published success", result)
			}
			a.mu.Lock()
			title := a.registrations[fixtureSession].title
			a.mu.Unlock()
			if title != nil && strings.Contains(string(*title), "new title") {
				t.Fatal("failed rename eagerly projected title")
			}
		})
	}
}
