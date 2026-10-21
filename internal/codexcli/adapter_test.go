package codexcli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

const firstID = "019a0000-0000-7000-8000-000000000001"
const secondID = "019a0000-0000-7000-8000-000000000002"

type historyFixture struct {
	calls atomic.Int32
	list  func(string) (protocol.SessionPage, error)
	read  func(string, string, bool, []string) (protocol.ConversationPage, error)
}

func (f *historyFixture) List(_ context.Context, cursor string) (protocol.SessionPage, error) {
	f.calls.Add(1)
	if f.list != nil {
		return f.list(cursor)
	}
	return protocol.SessionPage{Sessions: []protocol.Session{{ID: firstID, Title: "Native history"}}}, nil
}

func (f *historyFixture) Read(_ context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	f.calls.Add(1)
	if f.read != nil {
		return f.read(id, cursor, activity, excluded)
	}
	return protocol.ConversationPage{Messages: []protocol.Message{{MessageIdentity: protocol.MessageIdentity{
		ID: "native-item", TurnID: "native-turn", Role: "assistant"}, Text: "native answer"}},
		Runtime: &protocol.Runtime{Status: "notLoaded"}}, nil
}

func newFixture(t *testing.T, history *historyFixture, scope func(context.Context) (string, error)) *Adapter {
	t.Helper()
	if scope == nil {
		scope = func(context.Context) (string, error) { return "verified-scope", nil }
	}
	adapter, err := New(Options{History: history, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adapter.Close)
	return adapter
}

func TestHistoryWorksWithoutDaemonAndNeverAdvertisesExecution(t *testing.T) {
	f := &historyFixture{}
	a := newFixture(t, f, nil)
	if f.calls.Load() != 0 {
		t.Fatal("construction read native history")
	}
	status := a.Status(context.Background())
	if status.State != "connected" || status.DaemonState != "disconnected" || !status.Capabilities.History ||
		status.Capabilities.Live || status.Capabilities.IdleSend || status.Capabilities.Create || status.Capabilities.Stop ||
		status.Capabilities.Models || status.Capabilities.Usage || status.Capabilities.ActiveSend != "unsupported" {
		t.Fatalf("unproven capabilities: %+v", status)
	}
	list, err := a.List(context.Background(), "")
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].Access == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	page, err := a.Read(context.Background(), firstID, "", true, nil)
	if err != nil || page.Messages[0].Text != "native answer" || page.Messages[0].ID != "native-item" ||
		page.Runtime.Status != "notLoaded" || page.Runtime.ActiveTurnID != nil {
		t.Fatalf("history: %+v %v", page, err)
	}
	if !reflect.DeepEqual(list.Sessions[0].Access, page.Access) {
		t.Fatal("read changed canonical identity")
	}
	encoded, _ := json.Marshal([3]string{"codex", "verified-scope", firstID})
	digest := sha256.Sum256(encoded)
	if page.Access.CanonicalSessionRef != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatal("canonical reference differs from shared Desktop/CLI formula")
	}
	before := f.calls.Load()
	if _, err := a.Models(context.Background()); err == nil {
		t.Fatal("missing daemon supplied models")
	}
	if _, err := a.Usage(context.Background()); err == nil {
		t.Fatal("missing daemon supplied usage")
	}
	if f.calls.Load() != before {
		t.Fatal("metadata failure fell back to history")
	}
}

func TestEveryMutationAndLiveAttachRefusesBeforeNativeAccess(t *testing.T) {
	f := &historyFixture{}
	a := newFixture(t, f, nil)
	ctx := context.Background()
	_, _, createErr := a.Create(ctx, "input", "client", protocol.SessionSettings{})
	_, sendErr := a.Send(ctx, firstID, "input", "client")
	_, attachErr := a.Attach(ctx, firstID)
	_, renameErr := a.Rename(ctx, firstID, "name")
	_, archiveErr := a.Archive(ctx, firstID)
	for _, err := range []error{createErr, sendErr, attachErr, renameErr, archiveErr,
		a.SetModel(ctx, firstID, "model"), a.Stop(ctx, firstID, "turn"),
		a.UpdateSettings(ctx, firstID, protocol.SessionSettings{}), a.ValidateAccess(ctx, firstID, "binding")} {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("execution not refused: %v", err)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("refused control read or mutated native state")
	}
}

func TestScopeChangeRejectsInFlightHistoryAndOldCursors(t *testing.T) {
	current := "scope-a"
	f := &historyFixture{}
	a := newFixture(t, f, func(context.Context) (string, error) { return current, nil })
	nativeCursor := "native-page-2"
	f.read = func(id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
		return protocol.ConversationPage{Messages: []protocol.Message{}, NextCursor: &nativeCursor}, nil
	}
	page, err := a.Read(context.Background(), firstID, "", false, nil)
	if err != nil || page.NextCursor == nil {
		t.Fatalf("first page: %v", err)
	}
	current = "scope-b"
	before := f.calls.Load()
	if _, err := a.Read(context.Background(), firstID, *page.NextCursor, false, nil); err == nil {
		t.Fatal("cross-account cursor accepted")
	}
	if f.calls.Load() != before {
		t.Fatal("foreign cursor touched new account")
	}
	current = "scope-a"
	if _, err := a.Read(context.Background(), secondID, *page.NextCursor, false, nil); err == nil {
		t.Fatal("cross-session cursor accepted")
	}
	f.read = func(string, string, bool, []string) (protocol.ConversationPage, error) {
		current = "scope-b"
		return protocol.ConversationPage{Messages: []protocol.Message{}}, nil
	}
	if _, err := a.Read(context.Background(), firstID, "", false, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("scope race: %v", err)
	}
	current = "scope-a"
	f.list = func(string) (protocol.SessionPage, error) {
		current = "scope-b"
		return protocol.SessionPage{Sessions: []protocol.Session{{ID: firstID}}}, nil
	}
	if _, err := a.List(context.Background(), ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("catalog scope race: %v", err)
	}
}

func TestNativePagingDelegatesExactlyAndBindsReturnedScope(t *testing.T) {
	f := &historyFixture{}
	a := newFixture(t, f, nil)
	next := "native-next"
	seen := []string{}
	f.read = func(id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
		if id != firstID || !activity || !reflect.DeepEqual(excluded, []string{"excluded-turn"}) {
			t.Fatal("history request changed")
		}
		seen = append(seen, cursor)
		return protocol.ConversationPage{Messages: []protocol.Message{}, NextCursor: &next}, nil
	}
	page, err := a.Read(context.Background(), firstID, "", true, []string{"excluded-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(context.Background(), firstID, *page.NextCursor, true, []string{"excluded-turn"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"", "native-next"}) {
		t.Fatal(seen)
	}
}

func TestUnverifiedClosedCancelledAndMalformedReadsDoNotTouchHistory(t *testing.T) {
	f := &historyFixture{}
	a := newFixture(t, f, func(context.Context) (string, error) { return "", errors.New("account unavailable") })
	if a.Status(context.Background()).State != "unavailable" {
		t.Fatal("unverified store ready")
	}
	if _, err := a.List(context.Background(), ""); err == nil {
		t.Fatal("unverified catalog read")
	}
	if f.calls.Load() != 0 {
		t.Fatal("unverified account read history")
	}
	b := newFixture(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Read(ctx, firstID, "", false, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := b.Read(context.Background(), "../native", "", false, nil); err == nil {
		t.Fatal("path accepted")
	}
	if _, err := b.List(context.Background(), strings.Repeat("x", 16385)); err == nil {
		t.Fatal("unbounded cursor accepted")
	}
	b.Close()
	b.Close()
	if _, err := b.List(context.Background(), ""); err == nil {
		t.Fatal("closed adapter read")
	}
	if f.calls.Load() != 0 {
		t.Fatal("rejected request read history")
	}
}

func TestHistoryBoundsAndIdentityFailuresNeverBecomePartialSuccess(t *testing.T) {
	f := &historyFixture{}
	a := newFixture(t, f, nil)
	f.list = func(string) (protocol.SessionPage, error) {
		return protocol.SessionPage{Sessions: make([]protocol.Session, 31)}, nil
	}
	if _, err := a.List(context.Background(), ""); err == nil {
		t.Fatal("unbounded catalog accepted")
	}
	f.read = func(string, string, bool, []string) (protocol.ConversationPage, error) {
		return protocol.ConversationPage{Messages: make([]protocol.Message, 129)}, nil
	}
	if _, err := a.Read(context.Background(), firstID, "", false, nil); err == nil {
		t.Fatal("unbounded history accepted")
	}
	f.read = func(string, string, bool, []string) (protocol.ConversationPage, error) {
		return protocol.ConversationPage{Messages: []protocol.Message{{Text: "no identity"}}}, nil
	}
	if _, err := a.Read(context.Background(), firstID, "", false, nil); err == nil {
		t.Fatal("invented native message identity")
	}
}
