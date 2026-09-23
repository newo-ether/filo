package codex

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/toolpreview"
)

func TestInvalidArrayLengthNeverErasesSnapshot(t *testing.T) {
	cases := []any{0.5, -1.0, math.NaN(), math.Inf(1), float64(1 << 32), "0.5", "1_0", "0x_2", "\u00852", map[string]any{}, []any{true}, []any{1.0, 2.0}}
	for _, length := range cases {
		state := map[string]any{"id": "task", "hostId": "local", "turns": []any{"history"}}
		patches := []any{map[string]any{"op": "replace", "path": []any{"turns", "length"}, "value": length}}
		if updated, err := ApplyDesktopPatches(state, patches); err == nil {
			t.Errorf("length %v accepted: %+v", length, updated)
		}
		if state["turns"].([]any)[0] != "history" {
			t.Fatalf("invalid length %v changed old snapshot", length)
		}
	}
}

func TestArrayLengthCoercionAndAllocationBounds(t *testing.T) {
	cases := []struct {
		value  any
		length int
	}{
		{nil, 0}, {false, 0}, {true, 1}, {2.0, 2}, {"2", 2}, {"\ufeff2\n", 2},
		{"0x2", 2}, {"0b10", 2}, {"0o2", 2}, {"2e0", 2}, {[]any{}, 0},
		{[]any{nil}, 0}, {[]any{[]any{"2"}}, 2},
	}
	for _, tc := range cases {
		state := map[string]any{"id": "task", "hostId": "local", "turns": []any{"history"}}
		updated, err := ApplyDesktopPatches(state, []any{map[string]any{"op": "replace", "path": []any{"turns", "length"}, "value": tc.value}})
		fields, _ := asMap(updated)
		turns, _ := fields["turns"].([]any)
		if err != nil || len(turns) != tc.length || (tc.length > 0 && turns[0] != "history") {
			t.Errorf("length %#v = %+v, %v", tc.value, turns, err)
		}
	}
	for _, patch := range []map[string]any{
		{"op": "replace", "path": []any{"turns", "length"}},
		{"op": "remove", "path": []any{"turns", "length"}},
		{"op": "replace", "path": []any{"turns", "length"}, "value": float64(1<<32 - 1)},
	} {
		state := map[string]any{"id": "task", "hostId": "local", "turns": []any{"history"}}
		if _, err := ApplyDesktopPatches(state, []any{patch}); err == nil {
			t.Errorf("invalid or unbounded patch accepted: %+v", patch)
		}
	}
}

func TestOrderedPatchAndFailedBatchPreservePriorRevision(t *testing.T) {
	state, _, err := nativejson.Read(strings.NewReader(`{"id":"task","hostId":"local","turns":[],"tool":{"z":"first","a":"second"}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(state)
	patches := []any{
		map[string]any{"op": "replace", "path": []any{"tool", "z"}, "value": "updated"},
		map[string]any{"op": "add", "path": []any{"tool", "b"}, "value": "last"},
	}
	updated, err := ApplyDesktopPatches(state, patches)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := asMap(updated)
	preview, _ := toolpreview.ToolPreview(fields["tool"])
	if preview != `{"z":"updated","a":"second","b":"last"}` {
		t.Fatalf("patch lost field order: %s", preview)
	}
	patches = append(patches, map[string]any{"op": "replace", "path": []any{"turns", "length"}, "value": 0.5})
	if _, err := ApplyDesktopPatches(state, patches); err == nil {
		t.Fatal("bad batch accepted")
	}
	after, _ := json.Marshal(state)
	if string(after) != string(before) {
		t.Fatal("failed batch mutated prior revision")
	}
}
