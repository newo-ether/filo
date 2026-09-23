package codex

import (
	"errors"
	"math"
	"strconv"

	"github.com/newo-ether/filo/internal/nativejson"
)

var forbiddenPatchKeys = map[string]bool{"__proto__": true, "prototype": true, "constructor": true}

func jsSafeInteger(value float64) bool {
	return value == math.Trunc(value) && math.Abs(value) <= 9007199254740991
}

func jsonArrayIndex(key any, length int) (int, bool) {
	if number, ok := key.(float64); ok {
		if jsSafeInteger(number) && number >= 0 && number < float64(length) {
			return int(number), true
		}
	} else if text, ok := nativejson.AsText(key); ok {
		index, err := strconv.Atoi(text)
		if err == nil && index >= 0 && index < length && strconv.Itoa(index) == text {
			return index, true
		}
	}
	return 0, false
}

type desktopPatch struct {
	op      string
	path    []any
	value   any
	present bool
	spent   *int
}

func (p desktopPatch) charge(elements, bytesEach int) error {
	if elements > (nativejson.MaxRetainedBytes-*p.spent)/bytesEach {
		return errors.New("Desktop patch allocation limit")
	}
	*p.spent += elements * bytesEach
	return nil
}

// ApplyDesktopPatches performs copy-on-write updates. A failed patch publishes no
// partial state, and ordered native tool properties retain their preview order.
// Allocation is bounded for the entire patch batch, including array length growth.
func ApplyDesktopPatches(state, patches any) (any, error) {
	list, ok := patches.([]any)
	if !ok || len(list) > 10000 {
		return nil, errors.New("Invalid desktop patches")
	}
	result, spent := state, 0
	for _, value := range list {
		fields, object := asMap(value)
		op, _ := nativejson.AsText(fields["op"])
		path, array := fields["path"].([]any)
		if !object || (op != "add" && op != "replace" && op != "remove") || !array || len(path) > 100 {
			return nil, errors.New("Unsupported desktop patch")
		}
		for _, key := range path {
			if forbiddenPatchKeys[jsCoalesce(key, "null")] {
				return nil, errors.New("Unsupported desktop patch")
			}
		}
		replacement, present := fields["value"]
		patch := desktopPatch{op: op, path: path, value: replacement, present: present, spent: &spent}
		var err error
		result, err = patch.apply(result, 0)
		if err != nil {
			return nil, err
		}
	}
	before, _ := asMap(state)
	after, ok := asMap(result)
	beforeID, beforeValid := nativejson.AsText(before["id"])
	afterID, afterValid := nativejson.AsText(after["id"])
	host, _ := nativejson.AsText(after["hostId"])
	if !ok || !beforeValid || !afterValid || beforeID != afterID || host != "local" {
		return nil, errors.New("Desktop identity changed")
	}
	return result, nil
}

func (p desktopPatch) apply(node any, depth int) (any, error) {
	if depth == len(p.path) {
		if p.op == "remove" {
			return nil, errors.New("Cannot remove state root")
		}
		return p.value, nil
	}
	key := p.path[depth]
	name := jsCoalesce(key, "null")
	if fields, object := asMap(node); object {
		if err := p.charge(len(fields)+1, 48); err != nil {
			return nil, err
		}
		old, exists := fields[name]
		if depth < len(p.path)-1 {
			if !exists {
				return nil, errors.New("Missing desktop patch path")
			}
			updated, err := p.apply(old, depth+1)
			if err != nil {
				return nil, err
			}
			return nativejson.CopyProperty(node, name, updated, false), nil
		}
		if p.op != "add" && !exists {
			return nil, errors.New("Missing patch target")
		}
		return nativejson.CopyProperty(node, name, p.value, p.op == "remove"), nil
	}
	array, ok := node.([]any)
	if !ok {
		return nil, errors.New("Missing desktop patch parent")
	}
	if depth < len(p.path)-1 {
		if index, exists := jsonArrayIndex(key, len(array)); exists {
			if err := p.charge(len(array), 16); err != nil {
				return nil, err
			}
			updated, err := p.apply(array[index], depth+1)
			if err != nil {
				return nil, err
			}
			next := append([]any{}, array...)
			next[index] = updated
			return next, nil
		}
		if name == "length" {
			return nil, errors.New("Missing desktop patch parent")
		}
		return nil, errors.New("Missing desktop patch path")
	}
	if number, ok := key.(float64); ok {
		if !jsSafeInteger(number) || number < 0 || number > float64(len(array)) ||
			(p.op != "add" && number == float64(len(array))) {
			return nil, errors.New("Invalid patch index")
		}
		if err := p.charge(len(array)+1, 16); err != nil {
			return nil, err
		}
		index := int(number)
		next := append([]any{}, array...)
		switch p.op {
		case "add":
			next = append(next, nil)
			copy(next[index+1:], next[index:])
			next[index] = p.value
		case "remove":
			next = append(next[:index], next[index+1:]...)
		default:
			next[index] = p.value
		}
		return next, nil
	}
	if text, ok := nativejson.AsText(key); !ok || text != "length" {
		return nil, errors.New("Invalid array patch")
	}
	// Native modules execute in strict mode: deleting length throws, and assigning
	// length must agree with ToUint32 without truncating or clamping the value.
	if p.op == "remove" {
		return nil, errors.New("Cannot remove array length")
	}
	length, err := arrayLength(p.value, p.present)
	if err != nil {
		return nil, err
	}
	if err := p.charge(length, 16); err != nil {
		return nil, err
	}
	next := make([]any, length)
	copy(next, array)
	return next, nil
}
