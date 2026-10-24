package claude

import (
	"strconv"
	"strings"
)

// turn.step exposes the actual request effort, including numeric budgets. It is
// observation only: a request's value does not establish a session setting or
// the model's supported choices.
func validObservedEffort(value string) bool {
	switch value {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	if value == "" || len(value) > 16 || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return err == nil && number <= 9007199254740991 && strconv.FormatUint(number, 10) == value
}
