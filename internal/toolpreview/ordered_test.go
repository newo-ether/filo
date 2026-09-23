package toolpreview

import (
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

func TestNativeLeadingFieldsRemainVisible(t *testing.T) {
	value, _, err := nativejson.Read(strings.NewReader(`{"z_summary":"important summary","a_details":"`+strings.Repeat("x", 10000)+`"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	preview, _ := ToolPreview(value)
	if !strings.HasPrefix(preview, `{"z_summary":"important summary","a_details":`) ||
		!strings.HasSuffix(preview, nativejson.PreviewSuffix) {
		t.Fatalf("leading summary was displaced: %.100s", preview)
	}
}

func TestNativeEnumerationAndSurrogatesMatchStringify(t *testing.T) {
	const raw = `{"z":"first","10":"ten","2":"two","z":"replacement","01":"last","lone":"x\ud800y"}`
	value, _, err := nativejson.Read(strings.NewReader(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	preview, _ := ToolPreview(value)
	const want = `{"2":"two","10":"ten","z":"replacement","01":"last","lone":"x\ud800y"}`
	if preview != want {
		t.Fatalf("preview = %s, want %s", preview, want)
	}
	text := "<>&\u2028\u2029"
	preview, _ = ToolPreview(nativejson.NewObject(nativejson.Property{Name: "text", Value: text}))
	if preview != `{"text":"`+text+`"}` {
		t.Fatalf("extra escapes changed the preview budget: %q", preview)
	}
}
