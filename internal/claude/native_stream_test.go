package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func nativeLiveFixture(t *testing.T) *NativeSession {
	t.Helper()
	s, input := nativeFixture(t)
	done := make(chan error, 1)
	go func() { _, err := s.Send(context.Background(), "hello", "client"); done <- err }()
	<-input.writes
	nativeFixtureEcho(t, s, "hello")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return s
}

// These are the stream-json outer envelopes and nested raw API stream events,
// not flattened test-only notifications. Event UUIDs deliberately differ from
// message.id and tool_use.id so a fabricated native identity cannot pass.
func nativeLiveEvent(t *testing.T, s *NativeSession, event string) {
	t.Helper()
	nativeFixtureEvent(t, s, json.RawMessage(`{"type":"stream_event","uuid":"c445e323-0fe1-458a-8d13-704b7dfebcca","session_id":"`+s.id+`","parent_tool_use_id":null,"event":`+event+`}`))
}

func nativeLiveAssistant(t *testing.T, s *NativeSession, uuid, messageID, content string) {
	t.Helper()
	nativeFixtureEvent(t, s, json.RawMessage(`{"type":"assistant","uuid":"`+uuid+`","session_id":"`+s.id+`","parent_tool_use_id":null,"message":{"id":"`+messageID+`","role":"assistant","content":`+content+`}}`))
}

func nativeFindRow(t *testing.T, s *NativeSession, id string) protocol.Message {
	t.Helper()
	for _, row := range s.Read().Messages {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing native row %q", id)
	return protocol.Message{}
}

func TestNativeLiveTextReconcilesIndividualFinalBlocksWithoutDuplicates(t *testing.T) {
	s := nativeLiveFixture(t)
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"id":"msg_live_text","role":"assistant","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Par"}}`)
	id := "msg_live_text:block:0"
	row := nativeFindRow(t, s, id)
	if row.Text != "Par" || row.NativeID != "msg_live_text" || row.GroupID != "msg_live_text" || row.Activity != nil || row.TurnID != s.active.turn {
		t.Fatalf("partial identity/content=%+v", row)
	}
	nativeLiveEvent(t, s, `{"type":"content_block_stop","index":0}`)
	nativeLiveAssistant(t, s, "266848e3-be14-4ff5-862a-b10d71ed89d9", "msg_live_text", `[{"type":"text","text":"Final first block"}]`)
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"late ignored"}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Second"}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_stop","index":1}`)
	nativeLiveAssistant(t, s, "7263d818-9e3a-4dad-be9e-fb964d24ac78", "msg_live_text", `[{"type":"text","text":"Second complete"}]`)
	nativeLiveAssistant(t, s, "7263d818-9e3a-4dad-be9e-fb964d24ac78", "msg_live_text", `[{"type":"text","text":"Second complete"}]`)
	page := s.Read()
	if len(page.Messages) != 3 || page.Messages[1].Text != "Final first block" || page.Messages[2].Text != "Second complete" || page.Messages[2].ID != "msg_live_text:block:1" {
		t.Fatalf("final reconciliation=%+v", page.Messages)
	}
	nativeFixtureResult(t, s)
	if len(s.Read().Messages) != 3 || s.stream != nil {
		t.Fatal("terminal changed reconciled rows or retained stream")
	}
}

func TestNativeLiveThinkingToolArgumentsResultsAndSnapshot(t *testing.T) {
	s := nativeLiveFixture(t)
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"id":"msg_activity","role":"assistant","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Public thought"}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"NEVER_PROJECT_SIGNATURE"}}`)
	thought := nativeFindRow(t, s, "msg_activity:block:0")
	if thought.Text != "Public thought" || thought.Activity == nil || thought.Activity.Type != "thought" || thought.Activity.State != "running" || thought.NativeID != "msg_activity" {
		t.Fatalf("thought=%+v", thought)
	}
	nativeLiveEvent(t, s, `{"type":"content_block_stop","index":0}`)
	nativeLiveAssistant(t, s, "266848e3-be14-4ff5-862a-b10d71ed89d9", "msg_activity", `[{"type":"thinking","thinking":"Complete thought","signature":"NEVER_PROJECT_SIGNATURE"}]`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_native_1","name":"Read","input":{}}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":"}}`)
	tool := nativeFindRow(t, s, "toolu_native_1")
	if tool.Activity.Arguments.Value != `{"file_path":` || tool.NativeID != "toolu_native_1" || tool.GroupID != "msg_activity" || tool.Activity.State != "running" {
		t.Fatalf("partial tool=%+v", tool)
	}
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"demo.txt\"}"}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_stop","index":1}`)
	nativeLiveAssistant(t, s, "7263d818-9e3a-4dad-be9e-fb964d24ac78", "msg_activity", `[{"type":"tool_use","id":"toolu_native_1","name":"Read","input":{"file_path":"demo.txt"}}]`)
	nativeFixtureEvent(t, s, json.RawMessage(`{"type":"user","session_id":"`+s.id+`","uuid":"9453dbb8-ff20-4d5a-a04c-ff27b7c5ebbd","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_native_1","content":"denied","is_error":true}]}}`))
	page := s.Read()
	if len(page.Messages) != 3 || len(page.InputReceipts) != 1 {
		t.Fatal("activity created duplicate assistant or native input receipt")
	}
	tool = nativeFindRow(t, s, "toolu_native_1")
	if tool.Activity.State != "failed" || !tool.Activity.Result.Known || tool.Activity.Result.Value != `"denied"` || tool.Activity.Arguments.Value != `{"file_path":"demo.txt"}` {
		t.Fatalf("final tool=%+v", tool.Activity)
	}
	page.Messages[2].Activity.State = "reader mutation"
	*page.Messages[0].ClientID = "reader mutation"
	if nativeFindRow(t, s, "toolu_native_1").Activity.State != "failed" || s.Read().InputReceipts[0].ClientID != "client" || *s.Read().Messages[0].ClientID != "client" {
		t.Fatal("read payload aliases session state")
	}
	data, _ := json.Marshal(s.Read())
	if strings.Contains(string(data), "NEVER_PROJECT_SIGNATURE") {
		t.Fatal("thinking signature leaked")
	}
}

func TestNativeLiveBoundsDiscardWholePayloadAndFinalRestores(t *testing.T) {
	s := nativeLiveFixture(t)
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"id":"msg_bounds","role":"assistant","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"prefix"}}`)
	nativeLiveEvent(t, s, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, strings.Repeat("x", maxRowBytes)))
	nativeLiveEvent(t, s, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"suffix"}}`)
	if len(s.Read().Messages) != 1 {
		t.Fatal("overflow published a truncated prefix/suffix")
	}
	nativeLiveAssistant(t, s, "266848e3-be14-4ff5-862a-b10d71ed89d9", "msg_bounds", `[{"type":"text","text":"Complete native correction"}]`)
	if row := nativeFindRow(t, s, "msg_bounds:block:0"); row.Text != "Complete native correction" {
		t.Fatal("bounded authoritative final did not restore native row")
	}
	for index := 0; index < 200; index++ {
		nativeLiveEvent(t, s, fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"toolu_%d","name":"Read","input":{}}}`, index+1, index))
	}
	if len(s.stream.blocks) > maxRows || len(s.rows) > maxRows || s.rowBytes > maxSessionBytes {
		t.Fatal("native activity count/bytes exceeded bounds")
	}
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"id":"msg_huge_tool","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_big","name":"Read","input":{}}}`)
	nativeLiveEvent(t, s, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, strings.Repeat("x", maxRowBytes)))
	for _, row := range s.Read().Messages {
		if row.ID == "toolu_big" {
			t.Fatal("oversized arguments not bounded")
		}
	}
}

func TestNativeLiveIdentityMissingNestedForeignAndTerminalActivity(t *testing.T) {
	s := nativeLiveFixture(t)
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"role":"assistant","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"no identity"}}`)
	if len(s.rows) != 1 {
		t.Fatal("event UUID substituted for absent message identity")
	}
	nativeFixtureEvent(t, s, json.RawMessage(`{"type":"stream_event","session_id":"`+s.id+`","parent_tool_use_id":"toolu_parent","event":{"type":"message_start","message":{"id":"msg_child"}}}`))
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"child"}}`)
	if len(s.rows) != 1 {
		t.Fatal("subagent content borrowed parent execution identity")
	}
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"id":"msg_stop","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_stop","name":"Read","input":{}}}`)
	nativeFixtureResult(t, s)
	if nativeFindRow(t, s, "toolu_stop").Activity.State != "stopped" || s.stream != nil {
		t.Fatal("exact abort terminal left running activity")
	}
}

func TestNativeLiveFullFinalReconciliationAndForeignSession(t *testing.T) {
	s := nativeLiveFixture(t)
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"id":"msg_full","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"partial"}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"partial"}}`)
	final := `[{"type":"thinking","thinking":"Thought final"},{"type":"text","text":"Text final"}]`
	nativeLiveAssistant(t, s, "266848e3-be14-4ff5-862a-b10d71ed89d9", "msg_full", final)
	nativeLiveAssistant(t, s, "266848e3-be14-4ff5-862a-b10d71ed89d9", "msg_full", final)
	page := s.Read()
	if len(page.Messages) != 3 || page.Messages[1].Text != "Thought final" || page.Messages[2].Text != "Text final" || page.Messages[1].Activity.State != "succeeded" {
		t.Fatal("full final duplicated or failed to reconcile partial blocks")
	}
	var foreign map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"type":"stream_event","session_id":"2510bdc8-f214-4d0e-9ce0-d02c9f93b222","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"foreign"}}}`), &foreign); err != nil {
		t.Fatal(err)
	}
	if err := s.handle(foreign); err == nil || nativeFindRow(t, s, "msg_full:block:1").Text != "Text final" {
		t.Fatal("foreign stream identity changed native row")
	}
}

func TestNativeLiveConcurrentReadersOwnTheirActivitySnapshots(t *testing.T) {
	s := nativeLiveFixture(t)
	nativeLiveEvent(t, s, `{"type":"message_start","message":{"id":"msg_race","content":[]}}`)
	nativeLiveEvent(t, s, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for index := 0; index < 100; index++ {
			page := s.Read()
			for _, row := range page.Messages {
				if row.Activity != nil {
					row.Activity.State = "reader-owned"
				}
			}
		}
	}()
	for index := 0; index < 100; index++ {
		nativeLiveEvent(t, s, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"x"}}`)
	}
	readers.Wait()
	if row := nativeFindRow(t, s, "msg_race:block:0"); len(row.Text) != 100 || row.Activity.State != "running" {
		t.Fatal("reader mutated native incremental state")
	}
}
