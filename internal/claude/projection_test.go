package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEventBatchConflictDoesNotPartiallyApply(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	first := envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "row", UUID: fixtureRow, Role: "assistant", Text: "original"}}}
	bridgeCall(t, a, "push", first)
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
		Events: []modEvent{{Kind: "start", TurnID: "must-not-apply"},
			{Kind: "row", UUID: fixtureRow, Role: "assistant", Text: "replacement"}}})
	page, _ := a.Read(context.Background(), fixtureSession, "", false, nil)
	if status != http.StatusConflict || page.Runtime.ActiveTurnID != nil || page.Messages[0].Text != "original" {
		t.Fatal("rejected batch partially changed the native projection")
	}
}

func TestNativeTextPreservesEscapedLoneSurrogate(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	body := `{"sessionId":"` + fixtureSession + `","nonce":"` + nonce + `","sequence":1,"events":[{"kind":"row","uuid":"` + fixtureRow + `","role":"assistant","text":"\ud800"}]}`
	request := httptest.NewRequest(http.MethodPost, bridgePrefix+"push", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1"
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	writer := httptest.NewRecorder()
	a.ServeHTTP(writer, request)
	page, _ := a.Read(context.Background(), fixtureSession, "", false, nil)
	data, err := json.Marshal(page)
	if writer.Code != http.StatusOK || err != nil || !strings.Contains(string(data), `\ud800`) {
		t.Fatalf("native UTF-16 changed: %s %v", data, err)
	}
}

func TestDescriptorAndNativeStatusUsePublicEnums(t *testing.T) {
	a := fixtureAdapter(t)
	if a.Descriptor().Origin != "cli" {
		t.Fatal("Claude descriptor origin differs from the frozen public enum")
	}
	nonce := registerFixture(t, a, fixtureSession)
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "start", TurnID: "native-main-turn"}}})
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if status != http.StatusOK || err != nil || page.Runtime.Status != "active" {
		t.Fatal("native turn start did not expose the active runtime enum")
	}
	statuses, err := a.Statuses(context.Background(), []string{fixtureSession})
	if err != nil || len(statuses) != 1 || statuses[0].Status == nil || *statuses[0].Status != "active" {
		t.Fatal("status endpoint did not preserve the active runtime enum")
	}
}

func TestStaleCompletionCannotFinishAnotherNativeTurn(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "start", TurnID: "first"}, {Kind: "start", TurnID: "second"},
			{Kind: "complete", TurnID: "first"}}})
	page, _ := a.Read(context.Background(), fixtureSession, "", false, nil)
	if status != http.StatusOK || page.Runtime.Status != "active" || page.Runtime.ActiveTurnID == nil ||
		*page.Runtime.ActiveTurnID != "second" || page.Runtime.CompletedTurnID.Known {
		t.Fatal("stale completion settled another turn")
	}
}
