package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

const fixtureSession = "10000000-0000-4000-8000-000000000001"
const fixtureOther = "10000000-0000-4000-8000-000000000002"
const fixtureRow = "20000000-0000-4000-8000-000000000001"
const fixtureToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type nativeSurface interface {
	List(context.Context, string) (protocol.SessionPage, error)
	Create(context.Context, string, string, protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error)
	Models(context.Context) ([]protocol.Model, error)
	Read(context.Context, string, string, bool, []string) (protocol.ConversationPage, error)
	Attach(context.Context, string) (func(), error)
	Send(context.Context, string, string, string) (protocol.SendReceipt, error)
	SetModel(context.Context, string, string) error
	Stop(context.Context, string, string) error
	UpdateSettings(context.Context, string, protocol.SessionSettings) error
	Rename(context.Context, string, string) (protocol.Session, error)
	Archive(context.Context, string) (string, error)
}

var _ nativeSurface = (*Adapter)(nil)

func fixtureAdapter(t *testing.T) *Adapter {
	t.Helper()
	a, err := New(Config{Token: fixtureToken, NativeScopeID: "isolated-worker-scope"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func bridgeCall(t *testing.T, a http.Handler, action string, body any) (int, bridgeResponse) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, bridgePrefix+action, bytes.NewReader(data))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	writer := httptest.NewRecorder()
	a.ServeHTTP(writer, request)
	var response bridgeResponse
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return writer.Code, response
}

func registerFixture(t *testing.T, a *Adapter, session string) string {
	t.Helper()
	status, response := bridgeCall(t, a, "register", registerRequest{SessionID: session,
		Instance: "test-instance", Origin: "terminal", Version: "fixture"})
	if status != http.StatusOK || response.Nonce == "" {
		t.Fatalf("register: %d %+v", status, response)
	}
	return response.Nonce
}

func TestBridgeRequiresIndependentTokenLoopbackAndNoBrowserOrigin(t *testing.T) {
	a := fixtureAdapter(t)
	for _, tc := range []struct{ remote, token, origin string }{
		{"192.0.2.1:1", fixtureToken, ""}, {"127.0.0.1:1", "wrong-token", ""},
		{"127.0.0.1:1", fixtureToken, "https://example.com"}, {"not-an-address", fixtureToken, ""},
	} {
		request := httptest.NewRequest(http.MethodPost, bridgePrefix+"register", strings.NewReader(`{}`))
		request.RemoteAddr = tc.remote
		request.Header.Set("Authorization", "Bearer "+tc.token)
		request.Header.Set("Origin", tc.origin)
		writer := httptest.NewRecorder()
		a.ServeHTTP(writer, request)
		if writer.Code != http.StatusUnauthorized {
			t.Fatalf("accepted %+v", tc)
		}
	}
	page, _ := a.List(context.Background(), "")
	if len(page.Sessions) != 0 {
		t.Fatal("unauthorized native access")
	}
}

func TestRegistrationRotatesBindingAndCanonicalWithoutAccountProof(t *testing.T) {
	a := fixtureAdapter(t)
	first := registerFixture(t, a, fixtureSession)
	access, _ := a.Access(context.Background(), fixtureSession)
	second := registerFixture(t, a, fixtureSession)
	current, _ := a.Access(context.Background(), fixtureSession)
	if first == second || access.CanonicalSessionRef == current.CanonicalSessionRef {
		t.Fatal("registration assumed account continuity")
	}
	if a.ValidateAccess(context.Background(), fixtureSession, first) == nil {
		t.Fatal("old binding accepted")
	}
	if a.ValidateAccess(context.Background(), fixtureSession, second) != nil {
		t.Fatal("current binding refused")
	}
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: first,
		Sequence: 1, Events: []modEvent{{Kind: "start", TurnID: "old-turn"}}})
	if status != http.StatusConflict {
		t.Fatal("stale generation mutated state")
	}
	status, _ = bridgeCall(t, a, "register", registerRequest{SessionID: fixtureSession, Instance: "other-process", Origin: "terminal"})
	if status != http.StatusConflict {
		t.Fatal("other process took live registration")
	}
}

func TestNativeSessionAndNonceBothBindEveryEnvelope(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	registerFixture(t, a, fixtureOther)
	for _, action := range []string{"pull", "push", "revoke"} {
		status, _ := bridgeCall(t, a, action, envelope{SessionID: fixtureOther, Nonce: nonce,
			Sequence: 1, Events: []modEvent{{Kind: "start", TurnID: "turn-one"}}})
		if status != http.StatusConflict {
			t.Fatalf("%s cross-session accepted", action)
		}
	}
}

func TestTTLRevokeAndLifecycleInvalidateOldAccess(t *testing.T) {
	a := fixtureAdapter(t)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	nonce := registerFixture(t, a, fixtureSession)
	now = now.Add(DefaultTTL)
	if a.ValidateAccess(context.Background(), fixtureSession, nonce) == nil {
		t.Fatal("expired owner retained authority")
	}
	if a.Status().State != "unavailable" {
		t.Fatal("expired owner advertised connected")
	}
	for _, lifecycle := range []string{"clear", "resume", "branch", "reload", "exit"} {
		nonce = registerFixture(t, a, fixtureSession)
		status, _ := bridgeCall(t, a, "revoke", envelope{SessionID: fixtureSession, Nonce: nonce})
		if status != http.StatusOK {
			t.Fatalf("%s revoke failed", lifecycle)
		}
		if _, err := a.Access(context.Background(), fixtureSession); err == nil {
			t.Fatalf("%s retained registration", lifecycle)
		}
	}
}

func TestProjectionPreservesNativeUUIDAndDoesNotInferTurnOrReceipt(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "start", TurnID: "native-turn"}, {Kind: "row", UUID: fixtureRow, Role: "user", Text: "native text"}}})
	if status != http.StatusOK {
		t.Fatal("native event refused")
	}
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("read: %+v %v", page, err)
	}
	row := page.Messages[0]
	if row.ID != fixtureRow || row.NativeID != fixtureRow || row.TurnID != "" || row.ClientID != nil || len(page.InputReceipts) != 0 {
		t.Fatalf("fabricated native identity: %+v", row)
	}
	if page.Runtime.ActiveTurnID == nil || *page.Runtime.ActiveTurnID != "native-turn" {
		t.Fatal("lost native turn status")
	}
	if _, err := a.Read(context.Background(), fixtureSession, "older", false, nil); err == nil {
		t.Fatal("invented historic page")
	}
	if _, err := a.Send(context.Background(), fixtureSession, "send", fixtureOther); err == nil {
		t.Fatal("busy send queued")
	}
	if err := a.Stop(context.Background(), fixtureSession, "native-turn"); err == nil {
		t.Fatal("unqualified stop advertised")
	}
}

func TestEventSequenceDuplicateAndConflictAreBounded(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	input := envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "row", UUID: fixtureRow, Role: "assistant", Text: "hello"}}}
	for i := 0; i < 2; i++ {
		status, _ := bridgeCall(t, a, "push", input)
		if status != http.StatusOK {
			t.Fatal("identical read batch retry refused")
		}
	}
	input.Events[0].Text = "different"
	status, _ := bridgeCall(t, a, "push", input)
	if status != http.StatusConflict {
		t.Fatal("sequence content replacement accepted")
	}
	input.Sequence = 2
	status, _ = bridgeCall(t, a, "push", input)
	if status != http.StatusConflict {
		t.Fatal("native UUID reused for different text")
	}
	page, _ := a.Read(context.Background(), fixtureSession, "", false, nil)
	if len(page.Messages) != 1 || page.Messages[0].Text != "hello" {
		t.Fatal("duplicate native rows")
	}
}

func TestBoundsLimitSessionsBodiesAndRows(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	for i := 1; i <= maxRows+4; i++ {
		id := fmt.Sprintf("20000000-0000-4000-8000-%012d", i)
		status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: uint64(i),
			Events: []modEvent{{Kind: "row", UUID: id, Role: "assistant", Text: "row"}}})
		if status != http.StatusOK {
			t.Fatal("bounded event refused")
		}
	}
	page, _ := a.Read(context.Background(), fixtureSession, "", false, nil)
	if len(page.Messages) != maxRows || page.Messages[0].ID != "20000000-0000-4000-8000-000000000005" {
		t.Fatal("row bound failed")
	}
	for i := 2; i <= maxSessions+1; i++ {
		status, _ := bridgeCall(t, a, "register", registerRequest{SessionID: fmt.Sprintf("10000000-0000-4000-8000-%012d", i),
			Instance: "test-instance", Origin: "terminal"})
		if i <= maxSessions && status != http.StatusOK || i > maxSessions && status != http.StatusConflict {
			t.Fatal("session bound failed")
		}
	}
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: maxRows + 5,
		Events: []modEvent{{Kind: "row", UUID: fixtureOther, Role: "user", Text: protocol.Text(strings.Repeat("x", maxBody))}}})
	if status != http.StatusBadRequest {
		t.Fatal("oversized body accepted")
	}
}

func TestNoUnqualifiedMutationCapabilitiesOrImplicitCommands(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	access, err := a.Access(context.Background(), fixtureSession)
	if err != nil {
		t.Fatal(err)
	}
	caps := access.Capabilities
	if caps.Create || caps.IdleSend || caps.Stop || caps.Rename || caps.Archive || caps.Uploads || caps.ActiveSend != "unsupported" || len(caps.Settings) != 0 {
		t.Fatal("unproven mutation capability")
	}
	status, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusOK || len(response.Commands) != 0 || !response.ReadOnly {
		t.Fatal("implicit command dispatch")
	}
	if _, _, err := a.Create(context.Background(), "first", fixtureRow, protocol.SessionSettings{}); err == nil {
		t.Fatal("native creation attempted")
	}
	status, _ = bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "start", TurnID: "one"}, {Kind: "complete", TurnID: "one"}}})
	if status != http.StatusOK {
		t.Fatal("terminal observation refused")
	}
	if receipt, err := a.Send(context.Background(), fixtureSession, "first", fixtureRow); err == nil || receipt.ClientID != "" || receipt.TurnID != "" {
		t.Fatal("fake idle acceptance")
	}
}
