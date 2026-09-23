package nativejson

import (
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"
)

type repeatReader struct {
	b byte
	n int64
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.n {
		n = int(r.n)
	}
	for i := 0; i < n; i++ {
		p[i] = r.b
	}
	r.n -= int64(n)
	return n, nil
}

func obj(t *testing.T, v any) map[string]any {
	t.Helper()
	result, ok := Fields(v)
	if !ok {
		t.Fatalf("value is %T, not map", v)
	}
	return result
}

func arr(t *testing.T, v any) []any {
	t.Helper()
	result, ok := v.([]any)
	if !ok {
		t.Fatalf("value is %T, not slice", v)
	}
	return result
}

func mustRead(t *testing.T, input io.Reader, rollout bool) (any, int) {
	t.Helper()
	value, retained, err := Read(input, rollout)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return value, retained
}

const statePrefix = `{"id":"native","hostId":"local","cwd":"C:/project","latestModel":"native-model","turns":[{"turnId":"t","items":[`

func stateDocument(items string) string {
	return statePrefix + items + `]}]}`
}

func stateValue(t *testing.T, items string) map[string]any {
	t.Helper()
	value, _ := mustRead(t, strings.NewReader(stateDocument(items)), false)
	return obj(t, value)
}

func itemsOf(t *testing.T, state map[string]any) []any {
	t.Helper()
	turns := arr(t, state["turns"])
	return arr(t, obj(t, turns[0])["items"])
}

// Mirrors desktop-json.test.ts: a 192 MiB native tool result is consumed with a
// bounded retained projection while short public text and metadata survive.
func TestHugeToolResultStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("memory-heavy baseline case")
	}
	prefix := statePrefix + `{"id":"u","type":"userMessage","content":[{"type":"text","text":"Hello 🌍"}]},{"id":"tool","type":"mcpToolCall","result":{"content":[{"type":"text","text":"`
	suffix := `"}]}},{"id":"view","type":"imageView","path":"C:/image.png"}]}]}`
	size := int64(192 * 1024 * 1024)
	input := io.MultiReader(strings.NewReader(prefix), &repeatReader{b: 'x', n: size}, strings.NewReader(suffix))
	value, retained := mustRead(t, input, false)
	if retained > 4*1024*1024 {
		t.Fatalf("retained %d bytes for a 192 MiB payload", retained)
	}
	items := itemsOf(t, obj(t, value))
	if got := obj(t, value)["latestModel"]; got != "native-model" {
		t.Fatalf("latestModel = %v", got)
	}
	if got := Text(obj(t, arr(t, obj(t, items[0])["content"])[0])["text"]); got != "Hello 🌍" {
		t.Fatalf("public text = %q", got)
	}
	if got := Text(obj(t, items[2])["path"]); got != "C:/image.png" {
		t.Fatalf("image path = %q", got)
	}
	preview := Text(obj(t, arr(t, obj(t, obj(t, items[1])["result"])["content"])[0])["text"])
	if !strings.HasPrefix(preview, strings.Repeat("x", PreviewUnits)) || !strings.HasSuffix(preview, PreviewSuffix) {
		t.Fatalf("preview = %q", preview[:min(len(preview), 64)])
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(encoded) >= 10000 {
		t.Fatalf("projection serialized to %d bytes", len(encoded))
	}
}

// Mirrors the shared preview budget and discarded binary data assertions.
func TestSharedPreviewBudgetAndDiscardedFields(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"id":"tool","type":"mcpToolCall","arguments":{"operations":[`)
	for index := 0; index < 64; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"index":`)
		builder.WriteString(itoa(index))
		builder.WriteString(`,"code":"`)
		builder.WriteString(strings.Repeat("a", 10000))
		builder.WriteString(`"}`)
	}
	builder.WriteString(`]},"result":{"content":[{"data":"`)
	builder.WriteString(strings.Repeat("binary", 10000))
	builder.WriteString(`","type":"image"},{"text":"visible","type":"text"}]}}`)
	items := itemsOf(t, stateValue(t, builder.String()))
	item := obj(t, items[0])
	operations := arr(t, obj(t, item["arguments"])["operations"])
	if len(operations) != 64 {
		t.Fatalf("operations length %d", len(operations))
	}
	last := obj(t, operations[63])
	if last["index"] != float64(63) {
		t.Fatalf("operations[63].index = %v", last["index"])
	}
	if code := Text(last["code"]); code != "" {
		t.Fatalf("operations[63].code retained %d bytes after budget", len(code))
	}
	content := arr(t, obj(t, item["result"])["content"])
	if data := Text(obj(t, content[0])["data"]); data != "" {
		t.Fatalf("restore data retained %d bytes", len(data))
	}
	if kind := Text(obj(t, content[0])["type"]); kind != "image" {
		t.Fatalf("content type = %q", kind)
	}
	if visible := Text(obj(t, content[1])["text"]); visible != "visible" {
		t.Fatalf("content text = %q", visible)
	}
	firstCode := Text(obj(t, operations[0])["code"])
	if !strings.HasSuffix(firstCode, PreviewSuffix) || len(firstCode) > PreviewUnits+len(PreviewSuffix) {
		t.Fatalf("first code length %d", len(firstCode))
	}
}

// Native Immer sends path before value; the replacement value inherits that path
// and must therefore hit the same preview budget as the original structure.
func TestPatchValueInheritsImmerPath(t *testing.T) {
	wire := `{"params":{"change":{"patches":[{"op":"replace","path":["turns",0,"items",0,"arguments","operations",63,"code"],"value":"` +
		strings.Repeat("new", 10000) + `"}]}}}`
	value, _ := mustRead(t, strings.NewReader(wire), false)
	patches := arr(t, obj(t, obj(t, obj(t, value)["params"])["change"])["patches"])
	patch := obj(t, patches[0])
	patchPath := arr(t, patch["path"])
	if len(patchPath) != 8 || patchPath[6] != float64(63) {
		t.Fatalf("patch path = %v", patchPath)
	}
	replacement := Text(patch["value"])
	if !strings.HasPrefix(replacement, "new") || !strings.HasSuffix(replacement, PreviewSuffix) || len(replacement) > 8300 {
		t.Fatalf("patch value length %d not preview-bounded", len(replacement))
	}
}

func TestRolloutItemMappingAppliesPreviewPolicy(t *testing.T) {
	doc := `{"payload":{"item":{"result":"` + strings.Repeat("y", 20000) + `"}}}`
	projection, _ := mustRead(t, strings.NewReader(doc), true)
	result := Text(obj(t, obj(t, obj(t, projection)["payload"])["item"])["result"])
	if !strings.HasSuffix(result, PreviewSuffix) || len(result) > PreviewUnits+len(PreviewSuffix) {
		t.Fatalf("rollout result length %d not preview-bounded", len(result))
	}
	plain, _ := mustRead(t, strings.NewReader(doc), false)
	full := Text(obj(t, obj(t, obj(t, plain)["payload"])["item"])["result"])
	if len(full) != 20000 {
		t.Fatalf("non-rollout result length %d", len(full))
	}
}

// Discarded tails stay syntax validated; keys, nesting and literals fail closed.
func TestRejectionsStayFailClosed(t *testing.T) {
	oversizedKey := `{"` + strings.Repeat("k", 4097) + `":1}`
	if _, _, err := Read(strings.NewReader(oversizedKey), false); err == nil || !strings.Contains(err.Error(), "key/number limit") {
		t.Fatalf("oversized key error = %v", err)
	}
	nested := strings.Repeat("[", maxDepth+1) + "0" + strings.Repeat("]", maxDepth+1)
	if _, _, err := Read(strings.NewReader(nested), false); err == nil || !strings.Contains(err.Error(), "nesting limit") {
		t.Fatalf("deep nesting error = %v", err)
	}
	acceptable := strings.Repeat("[", maxDepth) + "0" + strings.Repeat("]", maxDepth)
	if _, _, err := Read(strings.NewReader(acceptable), false); err != nil {
		t.Fatalf("depth %d rejected: %v", maxDepth, err)
	}
	badEscape := `{"turns":[{"items":[{"result":"` + strings.Repeat("x", 10000) + `\q"}]}]}`
	if _, _, err := Read(strings.NewReader(badEscape), false); err == nil || !strings.Contains(err.Error(), "invalid desktop JSON escape") {
		t.Fatalf("discarded-tail escape error = %v", err)
	}
	if _, _, err := Read(strings.NewReader(`{"a":`), false); err == nil || !strings.Contains(err.Error(), "incomplete desktop JSON") {
		t.Fatalf("incomplete error = %v", err)
	}
	if _, _, err := Read(strings.NewReader(`{"a":1} {}`), false); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing error = %v", err)
	}
	if _, _, err := Read(strings.NewReader(`{"a":tru}`), false); err == nil || !strings.Contains(err.Error(), "literal") {
		t.Fatalf("literal error = %v", err)
	}
	if _, _, err := Read(strings.NewReader(`{"a":1,}`), false); err == nil {
		t.Fatal("trailing comma accepted")
	}
	control := `{"a":"x` + string([]byte{0x01}) + `y"}`
	if _, _, err := Read(strings.NewReader(control), false); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("control error = %v", err)
	}
}

func TestNumbersAndPrototypeKeys(t *testing.T) {
	value, _ := mustRead(t, strings.NewReader(`{"__proto__":{"polluted":true},"n":-0,"a":[null,false,true],"f":1.5,"e":1e3}`), false)
	object := obj(t, value)
	if _, ok := object["__proto__"]; !ok {
		t.Fatal("__proto__ key dropped")
	}
	if (map[string]any{})["polluted"] != nil {
		t.Fatal("prototype polluted")
	}
	if n := object["n"].(float64); n != 0 || !math.Signbit(n) {
		t.Fatalf("negative zero lost: %v", n)
	}
	literals := arr(t, object["a"])
	if len(literals) != 3 || literals[0] != nil || literals[1] != false || literals[2] != true {
		t.Fatalf("literal array = %v", literals)
	}
	if object["f"].(float64) != 1.5 || object["e"].(float64) != 1000 {
		t.Fatalf("numbers = %v %v", object["f"], object["e"])
	}
	if _, _, err := Read(strings.NewReader(`{"a":01}`), false); err == nil {
		t.Fatal("leading zero accepted")
	}
}

func itoa(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}
