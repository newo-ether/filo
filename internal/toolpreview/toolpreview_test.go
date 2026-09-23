package toolpreview

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

const (
	wtfHigh       = "\xed\xa0\x80"
	previewLimit  = nativejson.PreviewUnits
	previewSuffix = nativejson.PreviewSuffix
)

func TestNilHasNoPreview(t *testing.T) {
	if value, ok := ToolPreview(nil); ok || value != "" {
		t.Fatalf("nil preview = %q, %v", value, ok)
	}
}

func TestTopLevelStringStaysRaw(t *testing.T) {
	value, ok := ToolPreview("hello 🌍")
	if !ok || value != "hello 🌍" {
		t.Fatalf("preview = %q, %v", value, ok)
	}
}

func TestTopLevelLongStringTruncatesWithSingleSuffix(t *testing.T) {
	value, _ := ToolPreview(strings.Repeat("p", 10000))
	if !strings.HasPrefix(value, strings.Repeat("p", previewLimit)) ||
		!strings.HasSuffix(value, previewSuffix) ||
		strings.Count(value, previewSuffix) != 1 {
		t.Fatalf("preview boundary wrong: len=%d", len(value))
	}
	if len(value) != previewLimit+len(previewSuffix) {
		t.Fatalf("preview length = %d want %d", len(value), previewLimit+len(previewSuffix))
	}
}

func TestTrailingHighSurrogateRemovedBeforeSuffix(t *testing.T) {
	value, _ := ToolPreview(strings.Repeat("a", previewLimit-1) + wtfHigh + "z")
	if strings.Contains(value, wtfHigh) {
		t.Fatal("dangling high surrogate survived")
	}
	if !strings.HasSuffix(value, previewSuffix) ||
		!strings.HasSuffix(strings.TrimSuffix(value, previewSuffix), "aaaa") {
		t.Fatalf("preview tail = %q", value[len(value)-12:])
	}
}

func TestStructureMatchesStringify(t *testing.T) {
	value, _ := ToolPreview(map[string]any{"b": []any{1.0, "x"}, "a": "hi"})
	if value != `{"a":"hi","b":[1,"x"]}` {
		t.Fatalf("preview = %q", value)
	}
}

func TestEmptyStructuresAndNulls(t *testing.T) {
	cases := []struct {
		input any
		want  string
	}{
		{map[string]any{}, "{}"},
		{[]any{}, "[]"},
		{map[string]any{"a": nil}, `{"a":null}`},
		{[]any{nil}, "[null]"},
		{1.5, "1.5"},
		{true, "true"},
	}
	for _, testCase := range cases {
		value, _ := ToolPreview(testCase.input)
		if value != testCase.want {
			t.Fatalf("preview(%v) = %q want %q", testCase.input, value, testCase.want)
		}
	}
}

func TestInfinityBecomesNullLikeStringify(t *testing.T) {
	value, _ := ToolPreview(math.Inf(1))
	if value != "null" {
		t.Fatalf("infinity = %q", value)
	}
	value, _ = ToolPreview(map[string]any{"a": math.Inf(-1)})
	if value != `{"a":null}` {
		t.Fatalf("map infinity = %q", value)
	}
}

func TestHugeNestedStringTruncatesOnce(t *testing.T) {
	value, _ := ToolPreview(map[string]any{"result": strings.Repeat("y", 20000)})
	if strings.Count(value, previewSuffix) != 1 || !strings.HasSuffix(value, previewSuffix) {
		t.Fatalf("nested preview = %d bytes, suffix count %d", len(value), strings.Count(value, previewSuffix))
	}
	if !strings.HasPrefix(value, `{"result":"`) {
		t.Fatalf("nested preview head = %q", value[:20])
	}
}

func TestDepthLimitTruncates(t *testing.T) {
	var nested any = "leaf"
	for index := 0; index < 102; index++ {
		nested = []any{nested}
	}
	value, _ := ToolPreview(nested)
	if !strings.HasSuffix(value, previewSuffix) {
		t.Fatalf("deep preview tail = %q", value[len(value)-20:])
	}
	value, _ = ToolPreview([]any{[]any{"leaf"}})
	if value != `[["leaf"]]` {
		t.Fatalf("shallow = %q", value)
	}
}

func TestEscapesMatchStringify(t *testing.T) {
	value, _ := ToolPreview(map[string]any{"k": "a\"b\\\n\tx"})
	var roundTripped map[string]any
	if err := json.Unmarshal([]byte(value), &roundTripped); err != nil {
		t.Fatalf("preview is not valid json: %v", err)
	}
	if roundTripped["k"] != "a\"b\\\n\tx" {
		t.Fatalf("round trip = %q", roundTripped["k"])
	}
	if !strings.HasPrefix(value, `{"k":"a\"b\\\n\tx"`) {
		t.Fatalf("escaped preview = %q", value)
	}
}

func TestOversizedKeyTruncatesOnce(t *testing.T) {
	value, _ := ToolPreview(map[string]any{strings.Repeat("k", previewLimit+1): 1})
	if strings.Count(value, previewSuffix) != 1 || !strings.HasSuffix(value, previewSuffix) {
		t.Fatalf("oversized key preview len=%d", len(value))
	}
	if !strings.HasPrefix(value, `{"k`) {
		t.Fatalf("oversized key head = %q", value[:8])
	}
}

func TestMultibytePreviewRespectsUtf16Budget(t *testing.T) {
	value, _ := ToolPreview(map[string]any{"arguments": strings.Repeat("あ", 30000)})
	budget := utf16Len(strings.TrimSuffix(value, previewSuffix))
	if budget > previewLimit {
		t.Fatalf("preview exceeds UTF-16 budget: %d units", budget)
	}
	if !strings.HasSuffix(value, previewSuffix) {
		t.Fatalf("multibyte preview not truncated: %q", value[len(value)-40:])
	}
}
