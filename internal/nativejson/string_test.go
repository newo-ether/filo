package nativejson

import (
	"encoding/json"
	"strings"
	"testing"
)

func parseText(t *testing.T, jsonText string) any {
	t.Helper()
	value, _ := mustRead(t, strings.NewReader(`{"text":`+jsonText+`}`), false)
	return obj(t, value)["text"]
}

func TestPublicTextPreservesFragmentedUnicodeAndLoneSurrogates(t *testing.T) {
	var literal strings.Builder
	literal.WriteByte('"')
	for i := 0; i < 100; i++ {
		literal.WriteString(`中文🌍\n\"\\`)
	}
	literal.WriteString(`\ud800"`)
	parsed := parseText(t, literal.String())
	surrogate, ok := parsed.(SurrogateText)
	if !ok {
		t.Fatalf("expected SurrogateText, got %T", parsed)
	}
	got := string(surrogate)
	if !strings.HasSuffix(got, string([]byte{0xed, 0xa0, 0x80})) {
		t.Fatalf("escaped lone high surrogate not preserved as WTF-8: %q", got[len(got)-8:])
	}
	if !strings.Contains(got, "中文🌍\n") {
		t.Fatal("fragmented multibyte text damaged")
	}
	if strings.Contains(got, "\ufffd") {
		t.Fatal("U+FFFD substitution occurred")
	}
}

// The handoff gate: after parsing, DTO/JSON serialization must not replace the
// preserved lone surrogate with U+FFFD.
func TestSurrogateTextSerializesWithoutReplacement(t *testing.T) {
	parsed := parseText(t, `"a\ud800b\udfffc"`)
	surrogate, ok := parsed.(SurrogateText)
	if !ok {
		t.Fatalf("expected SurrogateText, got %T", parsed)
	}
	encoded, err := json.Marshal(map[string]any{"text": surrogate})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"text":"a\ud800b\udfffc"}`
	if string(encoded) != want {
		t.Fatalf("marshal = %s want %s", encoded, want)
	}
	roundTripped, _ := mustRead(t, strings.NewReader(string(encoded)), false)
	again := obj(t, roundTripped)["text"].(SurrogateText)
	if string(again) != string(surrogate) {
		t.Fatal("round trip changed surrogate preservation")
	}
	if strings.Contains(string(encoded), "\ufffd") {
		t.Fatal("serialization replaced surrogates")
	}
}

func TestProperPairsDecodeAndLoneLowSurrogateStays(t *testing.T) {
	paired := parseText(t, `"🌍\ud83c\udf0d"`)
	if _, ok := paired.(SurrogateText); ok {
		t.Fatalf("properly paired input returned SurrogateText: %T", paired)
	}
	if paired.(string) != "🌍🌍" {
		t.Fatalf("paired decode = %q", paired)
	}
	loneLow := parseText(t, `"\udc00x"`)
	value, ok := loneLow.(SurrogateText)
	if !ok {
		t.Fatalf("lone low surrogate collapsed to %T", loneLow)
	}
	if !strings.HasPrefix(string(value), string([]byte{0xed, 0xb0, 0x80})) || !strings.HasSuffix(string(value), "x") {
		t.Fatalf("lone low surrogate = %q", string(value))
	}
	brokenPair := parseText(t, `"\ud800A"`)
	broken, ok := brokenPair.(SurrogateText)
	if !ok || !strings.HasPrefix(string(broken), string([]byte{0xed, 0xa0, 0x80})) || !strings.HasSuffix(string(broken), "A") {
		t.Fatalf("broken pair = %q", string(broken))
	}
}

func TestPreviewSuffixAppearsOncePerSharedField(t *testing.T) {
	items := itemsOf(t, stateValue(t, `{"id":"t","type":"mcpToolCall","result":{"content":[{"type":"text","text":"`+
		strings.Repeat("p", 9000)+`"},{"type":"text","text":"`+strings.Repeat("q", 100)+`"}]}}`))
	content := arr(t, obj(t, obj(t, items[0])["result"])["content"])
	first := Text(obj(t, content[0])["text"])
	second := Text(obj(t, content[1])["text"])
	if strings.Count(first, PreviewSuffix) != 1 {
		t.Fatalf("first preview suffix count %d", strings.Count(first, PreviewSuffix))
	}
	if second != "" {
		t.Fatalf("exhausted budget retained %q", second)
	}
}
