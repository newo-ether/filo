package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This opt-in probe starts only a dedicated tool-free session. It prints protocol
// identity fields, never native configuration, credentials or transcript content.
func TestNativeStreamJSONProbe(t *testing.T) {
	if os.Getenv("FILO_CLAUDE_NATIVE_PROBE") != "1" {
		t.Skip("explicit native probe is disabled")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json",
		"--replay-user-messages", "--include-partial-messages", "--effort", "low", "--max-budget-usd", "0.25", "--tools", "",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--system-prompt", "You are a protocol verification assistant. Reply briefly.")
	command.SysProcAttr = nativeProcessAttributes()
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	encoder := json.NewEncoder(input)
	if err := encoder.Encode(map[string]any{"type": "control_request", "request_id": "filo-probe-init",
		"request": map[string]any{"subtype": "initialize"}}); err != nil {
		t.Fatal(err)
	}
	type received struct {
		value map[string]json.RawMessage
		err   error
	}
	messages := make(chan received)
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var value map[string]json.RawMessage
			if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
				messages <- received{err: err}
				return
			}
			messages <- received{value: value}
		}
		messages <- received{err: io.EOF}
	}()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	sent, echoed, completed := false, false, false
	results, echoes := 0, 0
	model, sessionID := "", ""
	modelAck, interruptSent, interruptAck, interruptTerminal := false, false, false, false
	for !completed {
		select {
		case <-deadline.C:
			input.Close()
			t.Fatal("dedicated native probe timed out; stdin closed without killing unrelated processes")
		case next := <-messages:
			if next.err != nil {
				_ = command.Wait()
				for _, known := range []string{"cannot be launched inside another Claude Code session", "requires --verbose", "Invalid MCP configuration", "not supported", "unknown option", "Invalid API key", "ENOENT", "EPERM", "EACCES", "operation not permitted", "spawn", "--tools", "--mcp-config", "--max-budget-usd", "Error:"} {
					if strings.Contains(diagnostic.String(), known) {
						t.Logf("native startup diagnostic matched %q", known)
					}
				}
				t.Fatalf("native probe ended: %v diagnosticBytes=%d", next.err, diagnostic.Len())
			}
			var kind, subtype string
			json.Unmarshal(next.value["type"], &kind)
			json.Unmarshal(next.value["subtype"], &subtype)
			if kind == "stream_event" || kind == "system" && subtype != "init" {
				continue
			}
			if kind == "system" && subtype == "init" {
				json.Unmarshal(next.value["model"], &model)
				var id string
				json.Unmarshal(next.value["session_id"], &id)
				if sessionID != "" && sessionID != id {
					t.Fatal("native session changed between input turns")
				}
				sessionID = id
			}
			fields := make([]string, 0, len(next.value))
			for key := range next.value {
				fields = append(fields, key)
			}
			t.Logf("native type=%s subtype=%s fields=%v uuid=%s session=%s", kind, subtype, fields,
				next.value["uuid"], next.value["session_id"])
			if kind == "control_response" {
				var response struct {
					Subtype   string                     `json:"subtype"`
					RequestID string                     `json:"request_id"`
					Response  map[string]json.RawMessage `json:"response"`
				}
				json.Unmarshal(next.value["response"], &response)
				keys := make([]string, 0, len(response.Response))
				for key := range response.Response {
					keys = append(keys, key)
				}
				t.Logf("control subtype=%s request=%s fields=%v", response.Subtype, response.RequestID, keys)
				if response.RequestID == "filo-probe-model" && response.Subtype == "success" {
					modelAck = true
					encoder.Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "Reply exactly FILO_SECOND_INPUT_OK. Do not use tools."}})
				}
				if response.RequestID == "filo-probe-interrupt" && response.Subtype == "success" {
					interruptAck = true
				}
				if response.RequestID == "filo-probe-init" && response.Subtype == "success" && !sent {
					sent = true
					err := encoder.Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "Reply exactly FILO_NATIVE_PROBE_OK. Do not use tools."}})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "user" && string(next.value["isReplay"]) == "true" {
				var uuid string
				json.Unmarshal(next.value["uuid"], &uuid)
				echoed = nativeUUID.MatchString(uuid)
				echoes++
				if results == 2 && !interruptSent {
					interruptSent = true
					encoder.Encode(map[string]any{"type": "control_request", "request_id": "filo-probe-interrupt", "request": map[string]any{"subtype": "interrupt"}})
				}
			}
			if kind == "result" {
				results++
				var failed bool
				json.Unmarshal(next.value["is_error"], &failed)
				t.Logf("native result number=%d failed=%v terminal_reason=%s stop_reason=%s", results, failed, next.value["terminal_reason"], next.value["stop_reason"])
				if results == 1 {
					encoder.Encode(map[string]any{"type": "control_request", "request_id": "filo-probe-model", "request": map[string]any{"subtype": "set_model", "model": model}})
				} else if results == 2 {
					encoder.Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "Write five hundred numbered lines with one sentence in every line. Do not use tools."}})
				} else {
					interruptTerminal = true
				}
			}
			completed = results == 3 && interruptAck
		}
	}
	input.Close()
	if err := command.Wait(); err != nil && !interruptTerminal {
		t.Errorf("dedicated native process exit before verified interrupt terminal: %v", err)
	}
	if !sent || !echoed || echoes != 3 || !modelAck || !interruptSent || !interruptAck || !interruptTerminal {
		t.Fatalf("native acceptance incomplete: sent=%v echoed=%v echoes=%d modelAck=%v interruptAck=%v terminal=%v", sent, echoed, echoes, modelAck, interruptAck, interruptTerminal)
	}
}
