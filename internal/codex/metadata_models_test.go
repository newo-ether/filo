package codex

import (
	"errors"
	"reflect"
	"testing"
)

// modelPagesFixture serves model/list pages keyed by the requested cursor.
type modelPagesFixture struct {
	pages  map[string]any
	params []map[string]any
}

func (f *modelPagesFixture) Request(method string, params any) (any, error) {
	if method != "model/list" {
		return nil, errors.New("unexpected method " + method)
	}
	values, _ := params.(map[string]any)
	f.params = append(f.params, values)
	cursor, _ := values["cursor"].(string)
	page, ok := f.pages[cursor]
	if !ok {
		return nil, errors.New("unknown cursor " + cursor)
	}
	return page, nil
}

func modelPage(next any, ids ...string) map[string]any {
	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{"model": id, "displayName": id, "isDefault": false})
	}
	return map[string]any{"data": data, "nextCursor": next}
}

func TestMetadataModelsFollowEveryCatalogPage(t *testing.T) {
	f := &modelPagesFixture{pages: map[string]any{
		"":   modelPage("p2", "a", "b"),
		"p2": modelPage("p3", "c"),
		"p3": modelPage(nil, "d"),
	}}
	models, err := NewNativeMetadata(f).Models()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "c", "d"}) {
		t.Fatalf("models = %v", ids)
	}
	// The first page keeps the historical empty request; later pages send only the cursor.
	want := []map[string]any{{}, {"cursor": "p2"}, {"cursor": "p3"}}
	if !reflect.DeepEqual(f.params, want) {
		t.Fatalf("params = %v", f.params)
	}
}

func TestMetadataModelsRejectANonAdvancingCursor(t *testing.T) {
	f := &modelPagesFixture{pages: map[string]any{
		"":     modelPage("loop", "a"),
		"loop": modelPage("loop", "b"),
	}}
	if _, err := NewNativeMetadata(f).Models(); err == nil || err.Error() != "Native model cursor did not advance" {
		t.Fatalf("err = %v", err)
	}
}
