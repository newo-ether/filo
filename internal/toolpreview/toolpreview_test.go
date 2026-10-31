package toolpreview

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

func TestCommonPayloadsRemainComplete(t *testing.T) {
	for _, size := range []int{9000, 24000, 128 * 1024} {
		text := strings.Repeat("x", size)
		for _, input := range []any{text, map[string]any{"content": text, "path": "file.txt"}, `{"content":"` + text + `","path":"file.txt"}`} {
			got, ok := ToolPreview(input)
			if !ok || strings.Contains(got, strings.TrimPrefix(nativejson.PreviewSuffix, "\n")) || !strings.Contains(got, text) {
				t.Fatalf("common payload changed at %d", size)
			}
		}
	}
}

func TestStructuredBoundsKeepValidJSONAndMetadata(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 2*limit), strings.Repeat("\"\\\n你好🌍", limit)} {
		input := nativejson.NewObject(nativejson.Property{Name: "output", Value: text}, nativejson.Property{Name: "exit_code", Value: float64(7)})
		got, _ := ToolPreview(input)
		var value map[string]any
		if len(got) > MaxBytes || json.Unmarshal([]byte(got), &value) != nil {
			t.Fatalf("invalid bounded object: %d bytes", len(got))
		}
		if value["exit_code"] != float64(7) || !strings.Contains(value["output"].(string), nativejson.PreviewSuffix) {
			t.Fatal("metadata or truncation marker lost")
		}
		fields, _ := nativejson.Fields(input)
		if nativejson.Text(fields["output"]) != text {
			t.Fatal("input mutated")
		}
	}
}

func TestNestedSerializedJSONAndArraysStayParseable(t *testing.T) {
	nested, _ := json.Marshal(map[string]any{"old_string": strings.Repeat("a", 2*limit), "new_string": "b"})
	got, _ := ToolPreview(map[string]any{"arguments": string(nested), "results": []any{"ok", float64(4)}})
	var value map[string]any
	if json.Unmarshal([]byte(got), &value) != nil {
		t.Fatal("outer JSON broken")
	}
	if !json.Valid([]byte(value["arguments"].(string))) {
		t.Fatal("nested JSON broken")
	}
}

func TestPlainTextAndSurrogateBoundaries(t *testing.T) {
	if got, ok := ToolPreview(nil); ok || got != "" {
		t.Fatal("nil changed")
	}
	if got, _ := ToolPreview("hello 🌍"); got != "hello 🌍" {
		t.Fatal("raw text changed")
	}
	got, _ := ToolPreview(strings.Repeat("a", limit-1) + "🌍tail")
	if !strings.HasSuffix(got, nativejson.PreviewSuffix) || strings.Contains(got, "\xed\xa0") {
		t.Fatal("invalid surrogate boundary")
	}
}

func TestStructureAndDepthRemainValid(t *testing.T) {
	got, _ := ToolPreview(map[string]any{"b": []any{1.0, "x"}, "a": "hi"})
	if got != `{"a":"hi","b":[1,"x"]}` {
		t.Fatalf("order: %s", got)
	}
	var nested any = "leaf"
	for index := 0; index < 102; index++ {
		nested = []any{nested}
	}
	got, _ = ToolPreview(nested)
	if !json.Valid([]byte(got)) || !strings.Contains(got, strings.TrimPrefix(nativejson.PreviewSuffix, "\n")) {
		t.Fatal("depth limit broke JSON")
	}
	got, _ = ToolPreview(map[string]any{strings.Repeat("k", limit+1): 1})
	if !json.Valid([]byte(got)) || len(got) > MaxBytes {
		t.Fatal("key limit broke JSON")
	}
	got, _ = ToolPreview(math.Inf(1))
	if got != "null" {
		t.Fatal("non-finite scalar changed")
	}
}
