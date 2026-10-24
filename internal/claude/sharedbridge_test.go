package claude

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func nativeOriginFixture(t *testing.T, origin string) *Adapter {
	t.Helper()
	adapter, err := New(Config{Token: fixtureToken, NativeScopeID: "scope-" + origin, Origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func registerOriginFixture(t *testing.T, bridge http.Handler, origin, id string) string {
	t.Helper()
	status, response := bridgeCall(t, bridge, "register", registerRequest{
		SessionID: id, Instance: "instance-" + origin, Origin: origin,
	})
	if status != http.StatusOK || response.Nonce == "" {
		t.Fatalf("%s registration: %d %+v", origin, status, response)
	}
	return response.Nonce
}

func TestSharedBridgeNativeOriginsHaveIndependentStatusAndIdentity(t *testing.T) {
	terminal, desktop := nativeOriginFixture(t, "terminal"), nativeOriginFixture(t, "desktop")
	bridge := SharedBridge(terminal, desktop)
	now := time.Unix(1000, 0)
	terminal.now, desktop.now = func() time.Time { return now }, func() time.Time { return now }
	terminalNonce := registerOriginFixture(t, bridge, "terminal", fixtureSession)
	if terminal.Status().State != "connected" || desktop.Status().State != "unavailable" {
		t.Fatal("terminal registration made Desktop connected")
	}
	now = now.Add(time.Second)
	desktopNonce := registerOriginFixture(t, bridge, "desktop", fixtureSession)
	terminalAccess, err := terminal.Access(context.Background(), fixtureSession)
	if err != nil {
		t.Fatal(err)
	}
	desktopAccess, err := desktop.Access(context.Background(), fixtureSession)
	if err != nil || terminalAccess.NativeScopeID == desktopAccess.NativeScopeID || terminalAccess.CanonicalSessionRef == desktopAccess.CanonicalSessionRef || terminalNonce == desktopNonce {
		t.Fatal("same native session ID shared source authority")
	}
	status, _ := bridgeCall(t, bridge, "push", envelope{SessionID: fixtureSession, Nonce: desktopNonce, Sequence: 1,
		Events: []modEvent{{Kind: "row", UUID: fixtureRow, Role: "assistant", Text: "desktop row"}}})
	terminalPage, terminalErr := terminal.Read(context.Background(), fixtureSession, "", false, nil)
	desktopPage, desktopErr := desktop.Read(context.Background(), fixtureSession, "", false, nil)
	if status != http.StatusOK || terminalErr != nil || desktopErr != nil || len(terminalPage.Messages) != 0 || len(desktopPage.Messages) != 1 {
		t.Fatal("shared bridge leaked native rows across origins")
	}
	if terminal.ValidateAccess(context.Background(), fixtureSession, desktopNonce) == nil || desktop.ValidateAccess(context.Background(), fixtureSession, terminalNonce) == nil {
		t.Fatal("native binding crossed origins")
	}
	now = now.Add(DefaultTTL - time.Second)
	if terminal.Status().State != "unavailable" || desktop.Status().State != "connected" {
		t.Fatal("one origin's TTL affected the other origin")
	}
	status, _ = bridgeCall(t, bridge, "revoke", envelope{SessionID: fixtureSession, Nonce: desktopNonce})
	if status != http.StatusOK || desktop.Status().State != "unavailable" {
		t.Fatal("Desktop revoke did not clear its own state")
	}
	registerOriginFixture(t, bridge, "desktop", fixtureOther)
	if terminal.Status().State != "unavailable" || desktop.Status().State != "connected" {
		t.Fatal("Desktop registration made terminal connected")
	}
	if _, err := terminal.Read(context.Background(), fixtureOther, "", false, nil); err == nil {
		t.Fatal("Desktop-only session leaked into terminal history")
	}
	if terminal.Descriptor().ID != "claude-code" || terminal.Descriptor().Origin != "cli" || desktop.Descriptor().ID != "claude-desktop" || desktop.Descriptor().Origin != "desktop" {
		t.Fatal("native source descriptor lost its origin")
	}
}

func TestSharedBridgeRejectsUnconfiguredOriginsAndCrossOriginReads(t *testing.T) {
	for _, selected := range []string{"terminal", "desktop"} {
		t.Run(selected, func(t *testing.T) {
			adapter := nativeOriginFixture(t, selected)
			bridge := SharedBridge(adapter)
			other := "desktop"
			if selected == "desktop" {
				other = "terminal"
			}
			status, _ := bridgeCall(t, bridge, "register", registerRequest{SessionID: fixtureOther, Instance: "unselected", Origin: other})
			if status != http.StatusForbidden || adapter.Status().State != "unavailable" {
				t.Fatal("unconfigured native origin registered")
			}
			registerOriginFixture(t, bridge, selected, fixtureSession)
			if _, err := adapter.Access(context.Background(), fixtureOther); err == nil {
				t.Fatal("unselected native session became readable")
			}
			if _, err := adapter.Read(context.Background(), fixtureOther, "", false, nil); err == nil {
				t.Fatal("unselected native history became readable")
			}
		})
	}
}
