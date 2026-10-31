package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

// These synthetic golden responses were captured from the retained 3ccc86ba
// DesktopJson -> applyDesktopPatches -> projectDesktop pipeline. The test itself
// uses only Go and fixture data; no legacy service/compiler/runtime is required.
func TestProjectionMatchesRetainedBehavior(t *testing.T) {
	file, err := os.Open("testdata/native-projection.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var fixture struct {
			Name     string          `json:"name"`
			RawState string          `json:"rawState"`
			Patches  json.RawMessage `json:"patches"`
			Limit    float64         `json:"limit"`
			Activity bool            `json:"activity"`
			Expected json.RawMessage `json:"expected"`
			Failed   bool            `json:"failed"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &fixture); err != nil {
			t.Fatal(err)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			state, _, err := nativejson.Read(strings.NewReader(fixture.RawState), false)
			if err == nil && len(fixture.Patches) > 0 && string(fixture.Patches) != "null" {
				var envelope any
				envelope, _, err = nativejson.Read(bytes.NewReader(append(append([]byte(`{"params":{"change":{"patches":`), fixture.Patches...), []byte(`}}}`)...)), false)
				root, _ := asMap(envelope)
				params, _ := asMap(root["params"])
				change, _ := asMap(params["change"])
				if err == nil {
					state, err = ApplyDesktopPatches(state, change["patches"])
				}
			}
			var actual []byte
			if err == nil {
				page, projectErr := ProjectDesktop(state, fixture.Activity, fixture.Limit)
				err = projectErr
				if err == nil {
					actual, err = json.Marshal(page)
				}
			}
			if fixture.Failed {
				if err == nil {
					t.Fatal("legacy rejection became a success")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, _, err := nativejson.Read(bytes.NewReader(fixture.Expected), false)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := nativejson.Read(bytes.NewReader(actual), false)
			if err != nil {
				t.Fatal(err)
			}
			// Old 8K prefix goldens intentionally contain broken JSON.
			// Refresh their tool fields from complete native data; all other public
			// fields and every unaffected golden retain exact equivalence checks.
			if fixture.Name == "mcp-budget" || fixture.Name == "escaped-budget" || fixture.Name == "emoji-cut" {
				var original map[string]any
				if err := json.Unmarshal([]byte(fixture.RawState), &original); err != nil {
					t.Fatal(err)
				}
				turn := original["turns"].([]any)[0].(map[string]any)
				item := turn["items"].([]any)[0].(map[string]any)
				wantRoot, _ := asMap(want)
				messages := wantRoot["messages"].([]any)
				message, _ := asMap(messages[0])
				activity, _ := asMap(message["activity"])
				args, _ := json.Marshal(item["arguments"])
				activity["arguments"] = string(args)
				if fixture.Name == "mcp-budget" {
					result := item["result"].(map[string]any)
					content := result["content"].([]any)[0].(map[string]any)
					output, _ := json.Marshal(map[string]any{"text": content["text"], "structuredContent": result["structuredContent"]})
					activity["result"] = string(output)
				}
				// Tool JSON key ordering is not semantic; compare decoded objects.
				gotRoot, _ := asMap(got)
				gotMessage, _ := asMap(gotRoot["messages"].([]any)[0])
				gotActivity, _ := asMap(gotMessage["activity"])
				for _, key := range []string{"arguments", "result"} {
					if activity[key] == nil {
						continue
					}
					for _, fields := range []map[string]any{activity, gotActivity} {
						text := nativejson.Text(fields[key])
						decoded, _, err := nativejson.Read(strings.NewReader(text), false)
						if err != nil {
							t.Fatalf("%s is not complete JSON", key)
						}
						fields[key] = decoded
					}
				}
			}
			if !reflect.DeepEqual(semanticJSON(got), semanticJSON(want)) {
				t.Fatal("public projection differs from expected native fields")
			}
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func semanticJSON(value any) any {
	if fields, ok := asMap(value); ok {
		result := make(map[string]any, len(fields))
		for key, child := range fields {
			result[key] = semanticJSON(child)
		}
		return result
	}
	if array, ok := value.([]any); ok {
		result := make([]any, len(array))
		for i, child := range array {
			result[i] = semanticJSON(child)
		}
		return result
	}
	if text, ok := nativejson.AsText(value); ok {
		return text
	}
	return value
}
