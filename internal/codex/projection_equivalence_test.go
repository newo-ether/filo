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
			if !reflect.DeepEqual(semanticJSON(got), semanticJSON(want)) {
				t.Fatalf("public projection differs\nactual: %s\nexpected: %s", actual, fixture.Expected)
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
