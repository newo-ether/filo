package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

func TestNativeLargeToolFieldsSurviveObservationWindow(t *testing.T) {
	s := nativeLiveFixture(t)
	args, _ := json.Marshal(map[string]any{"file_path": "fixture", "content": strings.Repeat("x", 128*1024)})
	nativeLiveAssistant(t, s, "266848e3-be14-4ff5-862a-b10d71ed89d9", "msg_large", `[{"type":"tool_use","id":"toolu_large","name":"Write","input":`+string(args)+`}]`)
	row := nativeFindRow(t, s, "toolu_large")
	if string(row.Activity.Arguments.Value) != string(args) {
		t.Fatal("common large arguments omitted or changed")
	}
	output, _ := json.Marshal(strings.Repeat("y", 128*1024))
	nativeFixtureEvent(t, s, json.RawMessage(`{"type":"user","session_id":"`+s.id+`","uuid":"9453dbb8-ff20-4d5a-a04c-ff27b7c5ebbd","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_large","content":`+string(output)+`,"is_error":true}]}}`))
	row = nativeFindRow(t, s, "toolu_large")
	if row.Activity.State != "failed" || string(row.Activity.Result.Value) != string(output) {
		t.Fatal("large output or native failure lost")
	}
	large, _ := json.Marshal(map[string]any{"output": strings.Repeat("z", 800000), "exit_code": 7})
	preview, _ := toolpreview.ToolPreview(string(large))
	if len(preview) > toolpreview.MaxBytes || !json.Valid([]byte(preview)) {
		t.Fatal("extreme tool result lost its JSON shape")
	}
	row.Activity.Result = protocol.Known(protocol.Text(preview))
	if !s.putNativeRow(row) {
		t.Fatal("bounded tool row omitted")
	}
}
