// Package toolpreview bounds tool result serialization like the desktop
// preview budget. Limits count UTF-16 units, matching the TypeScript original,
// so structured output cannot retain more than the preview by using multibyte text.
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

// ToolPreview serializes a bounded preview. A nil value has no preview.
// Native and projected objects retain their declared property order. Plain Go
// maps have no insertion order and are sorted for deterministic local fixtures.
func ToolPreview(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	result := ""
	truncated := false
	appendPart := func(part string) {
		remaining := limit - utf16Len(result)
		if utf16Len(part) > remaining {
			truncated = true
		}
		result += sliceUTF16(part, remaining)
	}
	var visit func(value any, depth int)
	visit = func(value any, depth int) {
		if truncated {
			return
		}
		if depth > 100 {
			truncated = true
			return
		}
		switch typed := value.(type) {
		case nativejson.SurrogateText:
			visit(string(typed), depth)
		case string:
			appendPart(jsonString(sliceUTF16(typed, limit+1)))
			if utf16Len(typed) > limit {
				truncated = true
			}
		case []any:
			appendPart("[")
			for index, item := range typed {
				if truncated {
					break
				}
				if index > 0 {
					appendPart(",")
				}
				visit(item, depth+1)
			}
			appendPart("]")
		case *nativejson.Object, map[string]any:
			fields, _ := nativejson.Fields(typed)
			if object, ok := typed.(*nativejson.Object); ok && object == nil {
				appendPart("null")
				return
			}
			appendPart("{")
			var keys []string
			if ordered, ok := typed.(*nativejson.Object); ok {
				keys = ordered.Keys()
			} else {
				for key := range fields {
					keys = append(keys, key)
				}
				sort.Strings(keys)
			}
			for keyIndex, key := range keys {
				if truncated {
					break
				}
				if keyIndex > 0 {
					appendPart(",")
				}
				appendPart(jsonString(sliceUTF16(key, limit+1)))
				if utf16Len(key) > limit {
					truncated = true
					break
				}
				appendPart(":")
				visit(fields[key], depth+1)
			}
			appendPart("}")
		default:
			appendPart(jsonLiteral(value))
		}
	}
	if text, ok := nativejson.AsText(value); ok {
		appendPart(text)
	} else {
		visit(value, 0)
	}
	if truncated {
		result = trimTrailingHighSurrogate(result)
		result += nativejson.PreviewSuffix
	}
	return result, true
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

// SliceUTF16 returns the leading units UTF-16 code units of s, matching
// String.prototype.slice semantics used by TS callers outside this package
// (for example the 8192-unit cap on turn error text).
func SliceUTF16(s string, units int) string { return sliceUTF16(s, units) }

// UTF16Len counts native UTF-16 code units, including preserved lone surrogates.
func UTF16Len(value string) int { return utf16Len(value) }
