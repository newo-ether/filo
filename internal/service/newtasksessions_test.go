package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// TestScopedBrowsingPreservesNativeCursorsAndNeverStartsAWriter mirrors 'ordinary
// browsing preserves native history cursors and never launches or resumes a writer'.
func TestScopedBrowsingPreservesNativeCursorsAndNeverStartsAWriter(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	page, err := fixture.sessions.Read(ctx, "ordinary", "cursor", true, []string{"covered"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if page.NextCursor == nil || *page.NextCursor != "native-cursor" {
		t.Fatalf("next cursor = %v, want native-cursor", page.NextCursor)
	}
	reads := fixture.history.history()
	if len(reads) != 1 || reads[0].id != "ordinary" || reads[0].cursor != "cursor" ||
		!reads[0].activity || len(reads[0].excluded) != 1 || reads[0].excluded[0] != "covered" {
		t.Fatalf("history reads = %+v", reads)
	}
	if opens, _ := fixture.factory.counts(); opens != 0 {
		t.Fatalf("open count = %d, want 0", opens)
	}
	if calls := fixture.peer.totalCalls(); calls != 0 {
		t.Fatalf("native calls = %d, want 0", calls)
	}
	if _, err := fixture.sessions.Send(ctx, "ordinary", "text", "client"); err == nil ||
		!strings.Contains(err.Error(), "outside") {
		t.Fatalf("send error = %v, want a creation scope refusal", err)
	}
	if calls := fixture.peer.totalCalls(); calls != 0 {
		t.Fatalf("native calls after a refused send = %d, want 0", calls)
	}
}

// TestScopedMutationsUseTheExactNativeTurn mirrors 'new task send/steer/stop use the
// exact native turn without querying an unsupported history index'.
func TestScopedMutationsUseTheExactNativeTurn(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	session, receipt, err := fixture.sessions.Create(ctx, "hello", "client", protocol.SessionSettings{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if session.ID != taskSessionsTaskID {
		t.Fatalf("created id = %q", session.ID)
	}
	if receipt.TurnID != taskSessionsTurnID || receipt.ClientID != "client" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if starts := fixture.peer.callCount("thread/start"); starts != 1 {
		t.Fatalf("thread starts = %d, want one native task started by the create", starts)
	}
	if starts := fixture.peer.callCount("turn/start"); starts != 1 {
		t.Fatalf("turn starts = %d, want the first turn run in the same create", starts)
	}
	page, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "cursor", true, nil)
	if err != nil {
		t.Fatalf("paged read: %v", err)
	}
	if page.NextCursor == nil || *page.NextCursor != "native-cursor" {
		t.Fatalf("next cursor = %v, want native-cursor", page.NextCursor)
	}
	if _, err := fixture.sessions.Send(ctx, taskSessionsTaskID, "adjust", "second"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	steers := fixture.peer.callsFor("turn/steer")
	if len(steers) != 1 {
		t.Fatalf("steers = %d, want 1", len(steers))
	}
	want := `{"clientUserMessageId":"second","expectedTurnId":"` + taskSessionsTurnID +
		`","input":[{"text":"adjust","text_elements":[],"type":"text"}],"threadId":"` + taskSessionsTaskID + `"}`
	if got := taskSessionsParams(t, steers[0]); got != want {
		t.Fatalf("steer params = %s, want %s", got, want)
	}
	if err := fixture.sessions.Stop(ctx, taskSessionsTaskID, "wrong"); err == nil ||
		!strings.Contains(err.Error(), "turn changed") {
		t.Fatalf("stop error = %v, want a changed turn refusal", err)
	}
	if interrupts := fixture.peer.callCount("turn/interrupt"); interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0", interrupts)
	}
	if err := fixture.sessions.Stop(ctx, taskSessionsTaskID, taskSessionsTurnID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if last := fixture.peer.lastMethod(); last != "turn/interrupt" {
		t.Fatalf("last native call = %q, want turn/interrupt", last)
	}
	if listing := fixture.peer.callCount("thread/turns/list"); listing != 0 {
		t.Fatalf("unsupported history index calls = %d, want 0", listing)
	}
	if opens, _ := fixture.factory.counts(); opens != 1 {
		t.Fatalf("open count = %d, want 1", opens)
	}
}

// TestScopedReadBridgesOnlyTheExactOwnedPendingRow mirrors 'only an exact owned
// pending catalog row is bridged by native events; storage errors still surface'.
func TestScopedReadBridgesOnlyTheExactOwnedPendingRow(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	if _, _, err := fixture.sessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	fixture.history.pendingIndex()
	fixture.peer.notify("item/agentMessage/delta", map[string]any{"threadId": taskSessionsTaskID,
		"turnId": taskSessionsTurnID, "itemId": "a", "delta": "stream"})
	fixture.settle(t)
	page, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "", true, nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	taskSessionsSameTexts(t, page.Messages, "hello", "stream")
	if page.Runtime == nil || !page.Runtime.ActiveTurnHasUserMessage.Known ||
		!page.Runtime.ActiveTurnHasUserMessage.Value {
		t.Fatalf("runtime = %+v, want a known user message in the active turn", page.Runtime)
	}
	var pending *history.NativeTaskNotIndexed
	if _, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "older", false, nil); !errors.As(err, &pending) {
		t.Fatalf("paged read error = %v, want NativeTaskNotIndexed", err)
	}
	fixture.history.failReads()
	if _, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "", false, nil); err == nil ||
		!strings.Contains(err.Error(), "Invalid native storage") {
		t.Fatalf("storage error = %v, want the native storage failure", err)
	}
	fixture.sessions.Close()
	if !fixture.peer.isActive() {
		t.Fatal("closing the helper must not interrupt accepted native work")
	}
}

// TestScopedSendIsNeverReplayedAfterAnUncertainReceipt mirrors 'an uncertain native
// send is not replayed or moved to a replacement writer'.
func TestScopedSendIsNeverReplayedAfterAnUncertainReceipt(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	fixture.peer.failNextSend()
	if _, _, err := fixture.sessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err == nil ||
		!strings.Contains(err.Error(), "Uncertain native receipt") {
		t.Fatalf("create error = %v, want the uncertain native receipt", err)
	}
	if starts := fixture.peer.callCount("turn/start"); starts != 1 {
		t.Fatalf("turn starts = %d, want 1", starts)
	}
	if opens, _ := fixture.factory.counts(); opens != 1 {
		t.Fatalf("open count = %d, want 1", opens)
	}
	fixture.sessions.Close()
	if !fixture.peer.isActive() {
		t.Fatal("closing the helper must not interrupt accepted native work")
	}
}

// TestScopedCreationRefusesALateNativeStartAfterClose mirrors 'closing during
// executor startup prevents a late native creation'.
func TestScopedCreationRefusesALateNativeStartAfterClose(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	fixture.factory.holdOpen()
	creation := make(chan error, 1)
	go func() {
		_, _, err := fixture.sessions.Create(context.Background(), "hello", "client", protocol.SessionSettings{})
		creation <- err
	}()
	<-fixture.factory.opening()
	fixture.sessions.Close()
	fixture.factory.releaseOpen()
	if err := <-creation; err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("create error = %v, want a shutdown refusal", err)
	}
	if calls := fixture.peer.totalCalls(); calls != 0 {
		t.Fatalf("native calls = %d, want 0", calls)
	}
}

// TestScopedStatusesNeverAllocateAWriterOrHydrateHistory mirrors 'scoped status
// reads never allocate a writer or hydrate active history'.
func TestScopedStatusesNeverAllocateAWriterOrHydrateHistory(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	statuses, err := fixture.sessions.Statuses(ctx, []string{taskSessionsTaskID})
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != nil {
		t.Fatalf("statuses = %+v, want one unknown status", statuses)
	}
	if opens, _ := fixture.factory.counts(); opens != 0 {
		t.Fatalf("open count = %d, want 0", opens)
	}
	if _, _, err := fixture.sessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	reads := len(fixture.history.history())
	statuses, err = fixture.sessions.Statuses(ctx, []string{taskSessionsTaskID})
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	if statuses[0].ActiveTurnID == nil || *statuses[0].ActiveTurnID != taskSessionsTurnID {
		t.Fatalf("active turn = %v, want %s", statuses[0].ActiveTurnID, taskSessionsTurnID)
	}
	if opens, _ := fixture.factory.counts(); opens != 1 {
		t.Fatalf("open count = %d, want 1", opens)
	}
	if after := len(fixture.history.history()); after != reads {
		t.Fatalf("history reads = %d, want %d", after, reads)
	}
}

// TestScopedRejectedSteerNeverStartsOrQueuesATurn mirrors 'a rejected steer never
// starts a new turn, queues, retries or opens another executor'.
func TestScopedRejectedSteerNeverStartsOrQueuesATurn(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	if _, _, err := fixture.sessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	fixture.peer.rejectSteer()
	fixture.peer.clearCalls()
	if _, err := fixture.sessions.Send(ctx, taskSessionsTaskID, "adjust", "second"); err == nil ||
		!strings.Contains(err.Error(), "Native active turn changed") {
		t.Fatalf("steer error = %v, want the native steer refusal", err)
	}
	if steers := fixture.peer.callCount("turn/steer"); steers != 1 {
		t.Fatalf("steers = %d, want 1", steers)
	}
	if starts := fixture.peer.callCount("turn/start"); starts != 0 {
		t.Fatalf("turn starts = %d, want 0", starts)
	}
	for _, call := range fixture.peer.calls() {
		if strings.HasPrefix(call.method, "thread/queue/") {
			t.Fatalf("a rejected steer queued %q", call.method)
		}
	}
	if opens, _ := fixture.factory.counts(); opens != 1 {
		t.Fatalf("open count = %d, want 1", opens)
	}
}

// TestScopedCompletedHistorySupersedesAPartialLiveDelta mirrors 'completed native
// history supersedes a partial live delta after a missed completion notification'.
func TestScopedCompletedHistorySupersedesAPartialLiveDelta(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	if _, _, err := fixture.sessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	fixture.peer.notify("item/agentMessage/delta", map[string]any{"threadId": taskSessionsTaskID,
		"turnId": taskSessionsTurnID, "itemId": "a", "delta": "Hel"})
	fixture.settle(t)
	live, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "", false, nil)
	if err != nil {
		t.Fatalf("live read: %v", err)
	}
	taskSessionsSameTexts(t, live.Messages, "hello", "Hel")
	fixture.peer.finish()
	fixture.history.complete([]protocol.Message{{
		MessageIdentity: protocol.MessageIdentity{ID: "a", TurnID: taskSessionsTurnID,
			Role: "assistant", Timestamp: protocol.Number(1000)},
		Text: protocol.Text("Hello"),
	}})
	completed, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "", false, nil)
	if err != nil {
		t.Fatalf("completed read: %v", err)
	}
	if completed.Runtime == nil || !completed.Runtime.CompletedTurnID.Known ||
		completed.Runtime.CompletedTurnID.Value == nil ||
		*completed.Runtime.CompletedTurnID.Value != taskSessionsTurnID {
		t.Fatalf("completed runtime = %+v, want the completed turn", completed.Runtime)
	}
	taskSessionsSameTexts(t, completed.Messages, "Hello")
	paged, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "older", false, nil)
	if err != nil {
		t.Fatalf("paged read: %v", err)
	}
	taskSessionsSameTexts(t, paged.Messages, "Hello")
}

// TestScopedLostConnectionForgetsItsEntryAndReportsTheChange mirrors the
// TypeScript `connection.rpc.once('closed')` cleanup: a lost connection drops the
// entry and reports the task, and the next read therefore follows again instead of
// reusing the closed connection.
func TestScopedLostConnectionForgetsItsEntryAndReportsTheChange(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	if _, _, err := fixture.sessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	fixture.rpc.Close()
	waitForCondition(t, 5*time.Second, "the lost connection to report its task", func() bool {
		return len(fixture.changes()) > 0
	})
	if _, follows := fixture.factory.counts(); follows != 0 {
		t.Fatalf("follows = %d before the read, want 0", follows)
	}
	if _, err := fixture.sessions.Read(ctx, taskSessionsTaskID, "", false, nil); err == nil {
		t.Fatal("a read on a lost connection must report the transport failure")
	}
	if _, follows := fixture.factory.counts(); follows != 1 {
		t.Fatalf("follows = %d, want 1", follows)
	}
}

// TestScopedStatusesRefuseMoreThanThirtyTasks verifies the bound the TypeScript
// adapter enforces without a test of its own.
func TestScopedStatusesRefuseMoreThanThirtyTasks(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ids := make([]string, 31)
	for index := range ids {
		ids[index] = taskSessionsTaskID
	}
	if _, err := fixture.sessions.Statuses(context.Background(), ids); err == nil ||
		!strings.Contains(err.Error(), "At most 30 listed sessions") {
		t.Fatalf("status error = %v, want the listed bound", err)
	}
	if opens, _ := fixture.factory.counts(); opens != 0 {
		t.Fatalf("open count = %d, want 0", opens)
	}
}

// TestScopedCreationAppliesDraftedSettingsBeforeTheFirstTurn pins the create
// contract: a drafted settings patch lands on the native task between the native
// start and the first turn, so the first turn already runs with it.
func TestScopedCreationAppliesDraftedSettingsBeforeTheFirstTurn(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	settings := protocol.SessionSettings{Model: protocol.Known("m"), Effort: protocol.Known("high")}
	_, receipt, err := fixture.sessions.Create(ctx, "hello", "client", settings)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if receipt.TurnID != taskSessionsTurnID || receipt.ClientID != "client" {
		t.Fatalf("receipt = %+v", receipt)
	}
	calls := fixture.peer.calls()
	names := make([]string, 0, len(calls))
	order := map[string]int{}
	for position, call := range calls {
		names = append(names, call.method)
		if _, seen := order[call.method]; !seen {
			order[call.method] = position
		}
	}
	for _, method := range []string{"thread/start", "model/list", "thread/settings/update", "turn/start"} {
		if _, seen := order[method]; !seen {
			t.Fatalf("native calls = %v, want one %s", names, method)
		}
	}
	if !(order["thread/start"] < order["model/list"] && order["model/list"] < order["thread/settings/update"] &&
		order["thread/settings/update"] < order["turn/start"]) {
		t.Fatalf("native calls = %v, want the settings between the native start and the first turn", names)
	}
	updates := fixture.peer.callsFor("thread/settings/update")
	want := `{"effort":"high","model":"m","threadId":"` + taskSessionsTaskID + `"}`
	if got := taskSessionsParams(t, updates[0]); got != want {
		t.Fatalf("settings params = %s, want %s", got, want)
	}
}

// TestScopedCreationRefusesADraftRejectedBeforeTheFirstTurn keeps a rejected
// settings patch from ever starting the first turn: the creation fails and the
// never-started native task is left for the native side to discard.
func TestScopedCreationRefusesADraftRejectedBeforeTheFirstTurn(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	settings := protocol.SessionSettings{Model: protocol.Known("unlisted-model")}
	if _, _, err := fixture.sessions.Create(ctx, "hello", "client", settings); err == nil ||
		!strings.Contains(err.Error(), "Model is unavailable") {
		t.Fatalf("create error = %v, want the settings refusal", err)
	}
	if starts := fixture.peer.callCount("turn/start"); starts != 0 {
		t.Fatalf("turn starts = %d, want none after a refused settings draft", starts)
	}
}
