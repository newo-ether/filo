package nativejson

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
)

// Object retains native property order. A map alone cannot reproduce which
// fields fit a bounded JSON.stringify preview. Numeric index keys enumerate
// first, ascending; all other keys retain their first insertion position.
type Object struct {
	fields map[string]any
	keys   []string
}

type Property struct {
	Name  string
	Value any
}

func NewObject(properties ...Property) *Object {
	object := &Object{fields: make(map[string]any, len(properties))}
	for _, property := range properties {
		object.set(property.Name, property.Value)
	}
	return object
}

func (o *Object) set(key string, value any) {
	if _, exists := o.fields[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.fields[key] = value
}

// Set is for constructing an unpublished object. Published native snapshots use
// CopyProperty, so patch failures cannot mutate a previously visible revision.
func (o *Object) Set(key string, value any) { o.set(key, value) }

// Fields exposes a read-only view for projection. Apply changes with
// CopyProperty so an existing snapshot and its ordering remain immutable.
func Fields(value any) (map[string]any, bool) {
	switch value := value.(type) {
	case *Object:
		if value != nil {
			return value.fields, true
		}
	case map[string]any:
		return value, true
	}
	return nil, false
}

func arrayKey(key string) (uint64, bool) {
	number, err := strconv.ParseUint(key, 10, 32)
	return number, err == nil && number < 1<<32-1 && strconv.FormatUint(number, 10) == key
}

func (o *Object) Keys() []string {
	indices, others := make([]string, 0), make([]string, 0, len(o.keys))
	for _, key := range o.keys {
		if _, ok := arrayKey(key); ok {
			indices = append(indices, key)
		} else {
			others = append(others, key)
		}
	}
	sort.Slice(indices, func(i, j int) bool {
		a, _ := arrayKey(indices[i])
		b, _ := arrayKey(indices[j])
		return a < b
	})
	return append(indices, others...)
}

// CopyProperty preserves plain-map fixtures and ordered native objects. Removing
// then re-adding a key places it last, while replacement keeps its old position.
func CopyProperty(value any, key string, replacement any, remove bool) any {
	fields, _ := Fields(value)
	next := make(map[string]any, len(fields)+1)
	for name, item := range fields {
		next[name] = item
	}
	if remove {
		delete(next, key)
	} else {
		next[key] = replacement
	}
	if ordered, ok := value.(*Object); ok {
		keys := make([]string, 0, len(next))
		for _, name := range ordered.keys {
			if !remove || name != key {
				keys = append(keys, name)
			}
		}
		if _, existed := fields[key]; !remove && !existed {
			keys = append(keys, key)
		}
		return &Object{fields: next, keys: keys}
	}
	return next
}

func (o *Object) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for i, key := range o.Keys() {
		if i > 0 {
			output.WriteByte(',')
		}
		name, err := SurrogateText(key).MarshalJSON()
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(o.fields[key])
		if err != nil {
			return nil, err
		}
		output.Write(name)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}
