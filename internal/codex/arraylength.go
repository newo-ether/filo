package codex

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/newo-ether/filo/internal/nativejson"
)

var decimalLength = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func arrayLength(value any, present bool) (int, error) {
	invalid := errors.New("Invalid array length")
	if !present {
		return 0, invalid
	}
	var number float64
	switch value := value.(type) {
	case nil:
		number = 0
	case bool:
		if value {
			number = 1
		}
	case float64:
		number = value
	default:
		text, ok := lengthPrimitive(value, 0)
		if !ok {
			return 0, invalid
		}
		text = trimJSWhitespace(text)
		if text == "" {
			number = 0
		} else if len(text) > 2 && text[0] == '0' && strings.ContainsRune("xXoObB", rune(text[1])) {
			base := 16
			if text[1] == 'o' || text[1] == 'O' {
				base = 8
			} else if text[1] == 'b' || text[1] == 'B' {
				base = 2
			}
			parsed, err := strconv.ParseUint(text[2:], base, 32)
			if err != nil {
				return 0, invalid
			}
			number = float64(parsed)
		} else {
			if !decimalLength.MatchString(text) {
				return 0, invalid
			}
			parsed, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return 0, invalid
			}
			number = parsed
		}
	}
	if !jsSafeInteger(number) || math.IsInf(number, 0) || number < 0 || number > 1<<32-1 {
		return 0, invalid
	}
	if number > float64(nativejson.MaxRetainedBytes/16) {
		return 0, errors.New("Desktop patch allocation limit")
	}
	return int(number), nil
}

// JSON arrays convert through Array.toString. Only a single element (possibly
// nested) can produce a valid numeric length; multiple elements introduce commas.
func lengthPrimitive(value any, depth int) (string, bool) {
	if depth > 100 {
		return "", false
	}
	if text, ok := nativejson.AsText(value); ok {
		return text, true
	}
	array, ok := value.([]any)
	if !ok || len(array) > 1 {
		return "", false
	}
	if len(array) == 0 || array[0] == nil {
		return "", true
	}
	if number, ok := array[0].(float64); ok {
		return jsNumberString(number), true
	}
	return lengthPrimitive(array[0], depth+1)
}
