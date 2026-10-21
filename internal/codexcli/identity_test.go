package codexcli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestCatalogSamplesAccountScopeOnceBeforeAndAfterThirtyRows(t *testing.T) {
	checks := 0
	f := &historyFixture{list: func(string) (protocol.SessionPage, error) {
		page := protocol.SessionPage{Sessions: make([]protocol.Session, 30)}
		for i := range page.Sessions {
			page.Sessions[i].ID = fmt.Sprintf("019a0000-0000-7000-8000-%012d", i+1)
		}
		return page, nil
	}}
	a := newFixture(t, f, func(context.Context) (string, error) {
		checks++
		return "scope-a", nil
	})
	page, err := a.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 30 || checks != 2 || f.calls.Load() != 1 {
		t.Fatalf("catalog admission amplified by row count: rows=%d checks=%d reads=%d err=%v",
			len(page.Sessions), checks, f.calls.Load(), err)
	}
	for _, row := range page.Sessions {
		if row.Access == nil || row.Access.NativeScopeID != "scope-a" || row.Access.Capabilities.IdleSend {
			t.Fatal("catalog row escaped its read-only captured scope")
		}
	}
}

func TestCatalogRejectsScopeChangeInsideRowIdentityProjection(t *testing.T) {
	scope := "scope-a"
	f := &historyFixture{}
	a, err := New(Options{History: f,
		Scope: func(context.Context) (string, error) { return scope, nil },
		SessionIdentity: func(context.Context, string) (protocol.SessionAccess, error) {
			scope = "scope-b"
			return protocol.SessionAccess{NativeScopeID: "scope-a", CanonicalSessionRef: "shared-ref", Binding: "read-only-binding"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.List(context.Background(), ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal("catalog published identity sampled before account replacement", err)
	}
}

func TestAccessRejectsScopeRotationInsideCommonIdentityCallback(t *testing.T) {
	scope := "scope-a"
	f := &historyFixture{}
	a, err := New(Options{History: f,
		Scope: func(context.Context) (string, error) { return scope, nil },
		SessionIdentity: func(context.Context, string) (protocol.SessionAccess, error) {
			scope = "scope-b"
			return protocol.SessionAccess{NativeScopeID: "scope-a", CanonicalSessionRef: "shared-ref", Binding: "shared-binding"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Access(context.Background(), firstID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("old identity escaped after account rotation", err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("identity lookup read native history")
	}
}

func TestStatusesRejectsBatchAcrossNativeScopes(t *testing.T) {
	scope := "scope-a"
	f := &historyFixture{}
	a := newFixture(t, f, func(context.Context) (string, error) { return scope, nil })
	reads := 0
	f.read = func(string, string, bool, []string) (protocol.ConversationPage, error) {
		reads++
		if reads == 1 {
			scope = "scope-b"
		}
		return protocol.ConversationPage{Messages: []protocol.Message{}}, nil
	}
	if _, err := a.Statuses(context.Background(), []string{firstID, secondID}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("mixed-scope status batch escaped", err)
	}
	if reads != 1 {
		t.Fatal("continued batch after scope rotation")
	}
}

func TestCatalogRejectsForeignStatusAndDaemonConfigurationIsNotImplicit(t *testing.T) {
	f := &historyFixture{list: func(string) (protocol.SessionPage, error) {
		return protocol.SessionPage{Sessions: []protocol.Session{{ID: firstID}}, Statuses: []protocol.SessionStatus{{ID: secondID}}}, nil
	}}
	a := newFixture(t, f, nil)
	if _, err := a.List(context.Background(), ""); err == nil {
		t.Fatal("foreign status identity accepted")
	}
	if _, err := New(Options{History: f, NativeScopeID: "scope-a"}); err == nil {
		t.Fatal("unverified static scope accepted")
	}
	if _, err := New(Options{History: f, Scope: func(context.Context) (string, error) { return "scope-a", nil }, SocketPath: "unverified"}); err == nil {
		t.Fatal("unverified daemon configured")
	}
}
