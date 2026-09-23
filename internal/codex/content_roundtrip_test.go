package codex

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

func projectedFrame(t *testing.T, raw string) protocol.ConversationPage {
	t.Helper()
	frame := make([]byte, len(raw)+4)
	binary.LittleEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	state, err := desktop.ReadFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	page, err := ProjectDesktop(state, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestNativeTextSurvivesFrameProjectionAndPublicJSON(t *testing.T) {
	const text = `prefix\ud800suffix中文🌍`
	page := projectedFrame(t, `{"id":"task","hostId":"local","turns":[{"turnId":"turn","status":"failed","error":{"message":"`+text+`"},"items":[`+
		`{"id":"user","type":"userMessage","content":[{"type":"text","text":"`+text+`"}]},`+
		`{"id":"answer","type":"agentMessage","text":"`+text+`"},`+
		`{"id":"thought","type":"reasoning","summary":["`+text+`"]},`+
		`{"id":"tool","type":"dynamicToolCall","tool":"inspect","arguments":{"z":"`+text+`"},"contentItems":[{"type":"text","text":"`+text+`"}],"success":true}]}]}`)
	encoded, err := json.Marshal(page)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("public JSON: %s, %v", encoded, err)
	}
	var wire protocol.ConversationPage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 5 {
		t.Fatalf("message count = %d", len(wire.Messages))
	}
	want := "prefix\xed\xa0\x80suffix中文🌍"
	for _, message := range wire.Messages {
		if message.ID != "tool" && string(message.Text) != want {
			t.Errorf("%s text changed: %q", message.ID, message.Text)
		}
		if message.ID == "tool" {
			if !message.Activity.Result.Known || string(message.Activity.Result.Value) != want {
				t.Errorf("tool result changed: %+v", message.Activity)
			}
			arguments, _, err := nativejson.Read(strings.NewReader(string(message.Activity.Arguments.Value)), false)
			fields, _ := asMap(arguments)
			if err != nil || nativejson.Text(fields["z"]) != want {
				t.Errorf("structured argument text changed: %v, %v", arguments, err)
			}
		}
	}
}

func TestToolProjectionKeepsLeadingTextWithinPreviewBudget(t *testing.T) {
	page := projectedFrame(t, `{"id":"task","hostId":"local","turns":[{"turnId":"turn","status":"completed","items":[`+
		`{"id":"mcp","type":"mcpToolCall","result":{"content":[{"type":"text","text":"important summary"}],"structuredContent":{"a_details":"`+strings.Repeat("x", 10000)+`"}}},`+
		`{"id":"command","type":"commandExecution","aggregatedOutput":"important output","exitCode":0,"status":"completed"}]}]}`)
	if got := page.Messages[0].Activity.Result.Value; !strings.HasPrefix(string(got), `{"text":"important summary","structuredContent":`) {
		t.Fatalf("MCP summary was displaced: %.100s", got)
	}
	if got := page.Messages[1].Activity.Result.Value; got != `{"output":"important output","exit_code":0}` {
		t.Fatalf("command output order = %s", got)
	}
}

func TestRPCResultPreservesTextAndNativeObjectOrder(t *testing.T) {
	f := newRpcFixture(t)
	request := f.requestAsync(t, "read")
	f.nextLine(t)
	f.feedLine(t, `{"id":1,"result":{"z":"prefix\ud800suffix","a":2}}`)
	result := awaitResult(t, request)
	fields, _ := asMap(result.value)
	if result.err != nil || nativejson.Text(fields["z"]) != "prefix\xed\xa0\x80suffix" {
		t.Fatalf("RPC text = %+v", result)
	}
	raw, err := json.Marshal(result.value)
	if err != nil || string(raw) != `{"z":"prefix\ud800suffix","a":2}` {
		t.Fatalf("RPC order/text = %s, %v", raw, err)
	}
}

func TestHostIdleUsesActualRPCDecodedState(t *testing.T) {
	for _, status := range []string{"idle", "active"} {
		f := newRpcFixture(t)
		done := make(chan requestResult, 1)
		go func() { count, err := VerifyHostIdle(f.rpc); done <- requestResult{count, err} }()
		if packet := f.nextLine(t); packet["method"] != "thread/loaded/list" {
			t.Fatal(packet)
		}
		f.feedLine(t, `{"id":1,"result":{"data":["task"],"nextCursor":null}}`)
		if packet := f.nextLine(t); packet["method"] != "thread/read" {
			t.Fatal(packet)
		}
		f.feedLine(t, `{"id":2,"result":{"thread":{"status":{"type":"`+status+`"}}}}`)
		result := awaitResult(t, done)
		if status == "idle" && (result.err != nil || result.value != 1) {
			t.Fatalf("idle host rejected: %+v", result)
		}
		if status == "active" && result.err == nil {
			t.Fatal("active host authorized as idle")
		}
	}
}
