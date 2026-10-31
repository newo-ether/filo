// Package toolpreview bounds serialized tool results in UTF-16 units, matching
// the desktop preview budget even when text uses multibyte UTF-8 encoding.
package toolpreview

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/newo-ether/filo/internal/nativejson"
)

const limit = nativejson.PreviewUnits

// MaxBytes bounds one serialized tool field, including JSON escaping.
const MaxBytes = 384 * 1024

// Read tool values without applying path-based projection budgets a second time.
// A wrapper keeps fields named "items" from being mistaken for native records.
func decodeToolJSON(text string) (any, error) {
	value, _, err := nativejson.Read(strings.NewReader(`{"toolValue":`+text+`}`), false)
	if err != nil {
		return nil, err
	}
	fields, _ := nativejson.Fields(value)
	return fields["toolValue"], nil
}

// ToolPreview bounds decoded values, never a serialized JSON prefix. JSON held
// inside a string is decoded first so arguments remain usable by native renderers.
func ToolPreview(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	if text, ok := nativejson.AsText(value); ok {
		if decoded, err := decodeToolJSON(text); err == nil {
			value = decoded
		} else {
			if json.Valid([]byte(text)) {
				return jsonString(nativejson.PreviewSuffix), true
			}
			return boundedText(text, limit), true
		}
	}
	for budget := limit; budget >= 1; budget /= 2 {
		nodes := 4096
		bounded := boundValue(value, budget, 0, &nodes)
		encoded := jsonLiteral(bounded)
		if len(encoded) <= MaxBytes {
			return encoded, true
		}
	}
	return jsonString(nativejson.PreviewSuffix), true
}

func boundedText(text string, budget int) string {
	if utf16Len(text) <= budget && len(text) <= budget {
		return text
	}
	prefix := trimTrailingHighSurrogate(sliceUTF16(text, budget))
	for len(prefix) > budget {
		_, size := utf8.DecodeLastRuneInString(prefix)
		prefix = prefix[:len(prefix)-size]
	}
	return prefix + nativejson.PreviewSuffix
}

func boundValue(value any, budget, depth int, nodes *int) any {
	*nodes--
	if depth > 64 || *nodes < 0 {
		return nativejson.SurrogateText(nativejson.PreviewSuffix)
	}
	if text, ok := nativejson.AsText(value); ok {
		// Nested JSON strings occur in native function arguments and MCP results.
		if len(text) > budget {
			if decoded, err := decodeToolJSON(text); err == nil {
				return nativejson.SurrogateText(jsonLiteral(boundValue(decoded, budget, depth+1, nodes)))
			}
			if json.Valid([]byte(text)) {
				return nativejson.SurrogateText(jsonString(nativejson.PreviewSuffix))
			}
		}
		return nativejson.SurrogateText(boundedText(text, budget))
	}
	switch typed := value.(type) {
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			if *nodes <= 0 {
				result = append(result, nativejson.SurrogateText(nativejson.PreviewSuffix))
				break
			}
			result = append(result, boundValue(item, budget, depth+1, nodes))
		}
		return result
	case *nativejson.Object, map[string]any:
		if object, ok := typed.(*nativejson.Object); ok && object == nil {
			return nil
		}
		fields, _ := nativejson.Fields(typed)
		var keys []string
		if ordered, ok := typed.(*nativejson.Object); ok {
			keys = ordered.Keys()
		} else {
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
		}
		result := nativejson.NewObject()
		for _, key := range keys {
			if *nodes <= 0 || len(key) > 4096 {
				result.Set("_filo_truncated", nativejson.PreviewSuffix)
				break
			}
			result.Set(key, boundValue(fields[key], budget, depth+1, nodes))
		}
		return result
	default:
		return value
	}
}

// sliceUTF16 returns the prefix counted in UTF-16 units. A cut that lands in
// the middle of a surrogate pair keeps the leading high surrogate as WTF-8,
// which the trailing cleanup below removes when the preview was truncated.
func sliceUTF16(s string, units int) string {
	if units < 0 {
		units = 0
	}
	count, i := 0, 0
	for i < len(s) {
		width, size := nextUnit(s, i)
		if count+width > units {
			if width == 2 && count < units {
				r, _ := utf8.DecodeRuneInString(s[i:])
				high, _ := utf16.EncodeRune(r)
				return s[:i] + wtf8Unit(uint16(high))
			}
			return s[:i]
		}
		count += width
		i += size
		if count >= units {
			return s[:i]
		}
	}
	return s
}

// nextUnit counts UTF-16 code units. A WTF-8 encoded surrogate code unit
// (produced by the native parser for unpaired surrogates) counts as one unit
// spanning three bytes, exactly like JavaScript string length.
func nextUnit(s string, i int) (units int, size int) {
	if i+2 < len(s) && s[i] == 0xED && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2]&0xC0 == 0x80 {
		return 1, 3
	}
	r, size := utf8.DecodeRuneInString(s[i:])
	if r > 0xFFFF {
		return 2, size
	}
	return 1, size
}

func utf16Len(value string) int {
	total, i := 0, 0
	for i < len(value) {
		units, size := nextUnit(value, i)
		total += units
		i += size
	}
	return total
}

// jsonString matches JSON.stringify byte-for-byte for strings (no HTML escaping).
func jsonString(value string) string {
	encoded, err := nativejson.SurrogateText(value).MarshalJSON()
	if err != nil {
		return "null"
	}
	return string(encoded)
}

// jsonLiteral matches JSON.stringify for scalars: non-finite numbers become null.
func jsonLiteral(value any) string {
	if text, ok := nativejson.AsText(value); ok {
		return jsonString(text)
	}
	if fields, ok := nativejson.Fields(value); ok {
		keys := make([]string, 0, len(fields))
		if ordered, ok := value.(*nativejson.Object); ok {
			keys = ordered.Keys()
		} else {
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
		}
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, jsonString(key)+":"+jsonLiteral(fields[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	if items, ok := value.([]any); ok {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			parts = append(parts, jsonLiteral(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	if number, ok := value.(float64); ok && (math.IsInf(number, 0) || math.IsNaN(number)) {
		return "null"
	}
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "null"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func trimTrailingHighSurrogate(s string) string {
	n := len(s)
	if n >= 3 && s[n-3] == 0xED && s[n-2] >= 0xA0 && s[n-2] <= 0xAF && s[n-1]&0xC0 == 0x80 {
		return s[:n-3]
	}
	return s
}

func wtf8Unit(unit uint16) string {
	return string([]byte{
		byte(0xE0 | unit>>12),
		byte(0x80 | (unit>>6)&63),
		byte(0x80 | unit&63),
	})
}

// SliceUTF16 returns the leading UTF-16 code units using String.prototype.slice
// semantics, including for callers that cap turn error text.
func SliceUTF16(s string, units int) string { return sliceUTF16(s, units) }

// UTF16Len counts native UTF-16 code units, including preserved lone surrogates.
func UTF16Len(value string) int { return utf16Len(value) }
