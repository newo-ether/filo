// Package nativejson reads native JSON without allocating discarded tool payloads.
// Limits apply to retained UTF-16 units, matching the original desktop projection.
package nativejson

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const (
	MaxRetainedBytes = 64 * 1024 * 1024
	PreviewUnits     = 8192
	PreviewSuffix    = "\n[Filo preview truncated; full output remains in Codex.]"
	maxDepth         = 100
)

type budget struct {
	remaining int
	marked    bool
}
type path []any

type decoder struct {
	r        *bufio.Reader
	retained int
	rollout  bool
	budgets  map[string]*budget
}

// Read consumes exactly one JSON document. The caller owns input size and lifetime.
// Tool strings are validated even after their retained prefix reaches its budget.
func Read(input io.Reader, rolloutItem bool) (any, int, error) {
	d := decoder{r: bufio.NewReaderSize(input, 64*1024), rollout: rolloutItem, budgets: make(map[string]*budget)}
	v, err := d.value(nil, 0)
	if err == nil {
		_, tail := d.next()
		if tail != io.EOF {
			err = errors.New("invalid trailing desktop JSON")
		}
	}
	return v, d.retained, err
}

func (d *decoder) charge(n int) error {
	d.retained += n
	if d.retained > MaxRetainedBytes {
		return errors.New("desktop retained state limit")
	}
	return nil
}

func (d *decoder) next() (byte, error) {
	for {
		b, err := d.r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return b, nil
		}
	}
}

func (d *decoder) value(p path, depth int) (any, error) {
	if err := d.charge(32); err != nil {
		return nil, err
	}
	b, err := d.next()
	if err != nil {
		return nil, fmt.Errorf("incomplete desktop JSON: %w", err)
	}
	switch b {
	case '{', '[':
		if depth >= maxDepth {
			return nil, errors.New("desktop JSON nesting limit")
		}
		if b == '{' {
			return d.object(p, depth+1)
		}
		return d.array(p, depth+1)
	case '"':
		limit, shared := d.stringPolicy(p)
		return d.string(limit, shared, false)
	case 't', 'f', 'n':
		literal, value := "true", any(true)
		if b == 'f' {
			literal, value = "false", false
		}
		if b == 'n' {
			literal, value = "null", nil
		}
		for i := 1; i < len(literal); i++ {
			actual, err := d.r.ReadByte()
			if err != nil || actual != literal[i] {
				return nil, errors.New("invalid desktop JSON literal")
			}
		}
		return value, nil
	default:
		if b != '-' && (b < '0' || b > '9') {
			return nil, errors.New("invalid desktop JSON value")
		}
		return d.number(b)
	}
}

func (d *decoder) object(p path, depth int) (*Object, error) {
	result := NewObject()
	b, err := d.next()
	if err != nil {
		return nil, err
	}
	if b == '}' {
		return result, nil
	}
	for {
		if b != '"' {
			return nil, errors.New("invalid desktop JSON object key")
		}
		keyValue, err := d.string(4096, nil, true)
		if err != nil {
			return nil, err
		}
		key := Text(keyValue)
		b, err = d.next()
		if err != nil || b != ':' {
			return nil, errors.New("invalid desktop JSON object separator")
		}
		childPath := appendPath(p, key)
		if d.rollout && len(childPath) == 2 && childPath[0] == "payload" && childPath[1] == "item" {
			childPath = path{"items", 0}
		}
		// Native Immer sends path before value. A replacement inherits that path.
		if len(childPath) == 5 && childPath[0] == "params" && childPath[1] == "change" &&
			childPath[2] == "patches" && childPath[4] == "value" {
			if target, ok := result.fields["path"].([]any); ok {
				childPath = normalizePath(target)
			}
		}
		value, err := d.value(childPath, depth)
		if err != nil {
			return nil, err
		}
		if _, exists := result.fields[key]; !exists {
			if err := d.charge(16); err != nil {
				return nil, err
			}
		}
		result.set(key, value)
		b, err = d.next()
		if err != nil {
			return nil, err
		}
		if b == '}' {
			return result, nil
		}
		if b != ',' {
			return nil, errors.New("invalid desktop JSON object delimiter")
		}
		b, err = d.next()
		if err != nil {
			return nil, err
		}
	}
}

func (d *decoder) array(p path, depth int) ([]any, error) {
	result := make([]any, 0)
	b, err := d.next()
	if err != nil {
		return nil, err
	}
	if b == ']' {
		return result, nil
	}
	if err = d.r.UnreadByte(); err != nil {
		return nil, err
	}
	for {
		value, err := d.value(appendPath(p, len(result)), depth)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		b, err = d.next()
		if err != nil {
			return nil, err
		}
		if b == ']' {
			return result, nil
		}
		if b != ',' {
			return nil, errors.New("invalid desktop JSON array delimiter")
		}
	}
}

func (d *decoder) number(first byte) (any, error) {
	data := []byte{first}
	for {
		b, err := d.r.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if (b >= '0' && b <= '9') || b == '.' || b == '-' || b == '+' || b == 'e' || b == 'E' {
			data = append(data, b)
			if len(data) > 1024 {
				return nil, errors.New("desktop JSON key/number limit")
			}
		} else {
			if err := d.r.UnreadByte(); err != nil {
				return nil, err
			}
			break
		}
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid desktop JSON number")
	}
	if err := d.charge(len(data) * 2); err != nil {
		return nil, err
	}
	return strconv.ParseFloat(string(data), 64)
}

func appendPath(p path, key any) path {
	result := make(path, len(p)+1)
	copy(result, p)
	result[len(p)] = key
	return result
}

func normalizePath(p []any) path {
	result := make(path, len(p))
	for i, key := range p {
		if n, ok := key.(float64); ok && n >= 0 && n <= float64(^uint(0)>>1) && float64(int(n)) == n {
			key = int(n)
		}
		result[i] = key
	}
	return result
}

func (d *decoder) stringPolicy(p path) (int, *budget) {
	for i := 0; i+2 < len(p); i++ {
		if p[i] != "items" {
			continue
		}
		if _, ok := p[i+1].(int); !ok {
			continue
		}
		fields := p[i+2:]
		first, _ := fields[0].(string)
		if first == "restoreMessage" || first == "raw_content" {
			return 0, nil
		}
		if first == "result" && len(fields) > 1 {
			if fields[1] == "_meta" {
				return 0, nil
			}
			if len(fields) > 3 && fields[1] == "content" {
				if _, ok := fields[2].(int); ok && fields[3] == "data" {
					return 0, nil
				}
				if fields[3] == "type" {
					return -1, nil
				}
			}
		}
		if first == "contentItems" && len(fields) > 2 && fields[2] == "type" {
			return -1, nil
		}
		if !previewField(first) {
			return -1, nil
		}
		encoded, _ := json.Marshal(p[:i+3])
		key := string(encoded)
		shared := d.budgets[key]
		if shared == nil {
			shared = &budget{remaining: PreviewUnits}
			d.budgets[key] = shared
		}
		return shared.remaining, shared
	}
	return -1, nil
}

func previewField(s string) bool {
	switch s {
	case "arguments", "result", "contentItems", "aggregatedOutput", "changes", "agentsStates", "results", "aggregated_output", "stdout", "stderr", "formatted_output":
		return true
	}
	return false
}
