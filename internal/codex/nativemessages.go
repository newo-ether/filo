package codex

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

// Native data flows as decoded JSON maps so surrogate-bearing text keeps the
// WTF-8 preservation guarantees of the nativejson decoder; TS reads these
// same fields via duck typing.

func asMap(v any) (map[string]any, bool) {
	m, ok := nativejson.Fields(v)
	return m, ok
}

// copyDefined mirrors how TS object literals drop keys whose value is
// undefined (absent in the source map) while keeping explicit nulls.
func copyDefined(dst *nativejson.Object, dstKey string, src map[string]any, srcKey string) {
	if src == nil {
		return
	}
	if v, exists := src[srcKey]; exists {
		dst.Set(dstKey, v)
	}
}

// inputText mirrors TS inputText: only non-string parts with type "text"
// contribute; raw strings inside the content array are dropped.
func inputText(content any) string {
	parts, ok := content.([]any)
	if !ok {
		// TS flatMap on a non-array raises a TypeError; native data always
		// sends arrays, and the empty join is the closest total mapping.
		parts = []any{}
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if pm, ok := asMap(part); ok {
			if t, _ := nativejson.AsText(pm["type"]); t == "text" {
				text, _ := nativejson.AsText(pm["text"]) // part.text ?? ''
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func encoded(value any) protocol.Optional[protocol.Text] {
	text, ok := toolpreview.ToolPreview(value)
	if !ok {
		return protocol.Optional[protocol.Text]{}
	}
	return protocol.Known(protocol.Text(text))
}

// durationMilliseconds mirrors the TS guard: finite, 0 <= v <= 2^53-1, then
// Math.trunc.
func durationMilliseconds(value any) (float64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if number < 0 || number > 9007199254740991 { // Number.MAX_SAFE_INTEGER
		return 0, false
	}
	return math.Trunc(number), true
}

// textContent mirrors the TS helper: array parts of type text/inputText with
// string text are joined with blank lines; empty results are undefined.
func textContent(value any) (string, bool) {
	parts, ok := value.([]any)
	if !ok {
		return "", false
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		// TS part?.type: non-object parts yield undefined property reads.
		if partMap, isMap := asMap(part); isMap {
			typeName, _ := nativejson.AsText(partMap["type"])
			if typeName == "text" || typeName == "inputText" {
				if text, isStr := nativejson.AsText(partMap["text"]); isStr {
					texts = append(texts, text)
				}
			}
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n\n"), true
}

// toolActivity normalizes public tool records and native image references,
// never binary data or raw reasoning. It mirrors the TS switch exactly,
// including which fields gate failure and result presence.
func toolActivity(item map[string]any) (*protocol.Activity, bool) {
	typeName, _ := nativejson.AsText(item["type"])
	var toolName string
	var args any
	var result any
	failed := isFalse(item["success"]) || item["error"] != nil
	switch typeName {
	case "commandExecution":
		toolName = "execute_shell_command"
		execArgs := nativejson.NewObject()
		copyDefined(execArgs, "command", item, "command")
		copyDefined(execArgs, "cwd", item, "cwd")
		args = execArgs
		if exitCode, isNum := item["exitCode"].(float64); isNum && exitCode != 0 {
			failed = true
		}
		aggregated, hasAggregated := item["aggregatedOutput"]
		exitCodeVal, hasExit := item["exitCode"]
		if (hasAggregated && aggregated != nil) || (hasExit && exitCodeVal != nil) {
			execResult := nativejson.NewObject()
			copyDefined(execResult, "output", item, "aggregatedOutput")
			copyDefined(execResult, "exit_code", item, "exitCode")
			result = execResult
		}
	case "fileChange":
		toolName = "file_edit"
		changeArgs := nativejson.NewObject()
		copyDefined(changeArgs, "changes", item, "changes")
		args = changeArgs
		if status, _ := nativejson.AsText(item["status"]); status == "completed" {
			changeResult := nativejson.NewObject()
			copyDefined(changeResult, "changes", item, "changes")
			result = changeResult
		}
	case "mcpToolCall":
		toolName = jsCoalesce(item["server"], "mcp") + "/" + jsCoalesce(item["tool"], "tool")
		args = item["arguments"]
		// TS: output = item.result as {content?, structuredContent?, isError?}|null.
		output := item["result"]
		if outputMap, isMap := asMap(output); isMap {
			if isError, isBool := outputMap["isError"].(bool); isBool && isError {
				failed = true
			}
		}
		// result = item.error ?? (output == null ? undefined :
		//   { text: textContent(output.content), structuredContent: output.structuredContent })
		// A truthy non-object output yields the empty projected object.
		errVal := item["error"]
		if errVal != nil {
			result = errVal
		} else if output != nil {
			mcpResult := nativejson.NewObject()
			if outputMap, isMap := asMap(output); isMap {
				if text, hasText := textContent(outputMap["content"]); hasText {
					mcpResult.Set("text", nativejson.SurrogateText(text))
				}
				copyDefined(mcpResult, "structuredContent", outputMap, "structuredContent")
			}
			result = mcpResult
		}
	case "dynamicToolCall":
		parts := []string{}
		for _, key := range []string{"namespace", "tool"} {
			if value, isStr := nativejson.AsText(item[key]); isStr && value != "" {
				parts = append(parts, value)
			}
		}
		toolName = strings.Join(parts, "/")
		if toolName == "" {
			toolName = "dynamicToolCall"
		}
		args = item["arguments"]
		if text, hasText := textContent(item["contentItems"]); hasText {
			result = text
		}
	case "collabAgentToolCall":
		toolName = "collaboration/" + jsCoalesce(item["tool"], "tool")
		collabArgs := nativejson.NewObject()
		copyDefined(collabArgs, "prompt", item, "prompt")
		copyDefined(collabArgs, "receiverThreadIds", item, "receiverThreadIds")
		copyDefined(collabArgs, "model", item, "model")
		args = collabArgs
		if states, hasStates := item["agentsStates"]; hasStates && states != nil {
			result = states
		}
	case "webSearch":
		toolName = "web_search"
		searchArgs := nativejson.NewObject()
		copyDefined(searchArgs, "query", item, "query")
		copyDefined(searchArgs, "action", item, "action")
		args = searchArgs
		if resultsVal, hasResults := item["results"]; hasResults && resultsVal != nil {
			result = nativejson.NewObject(nativejson.Property{Name: "results", Value: resultsVal})
		}
	case "imageView":
		toolName = "view_image"
		viewArgs := nativejson.NewObject()
		copyDefined(viewArgs, "path", item, "path")
		args = viewArgs
	case "imageGeneration":
		// result contains image bytes; only public text and saved-path
		// metadata belong in this slice.
		toolName = "imageGeneration"
		genArgs := nativejson.NewObject()
		copyDefined(genArgs, "prompt", item, "revisedPrompt")
		copyDefined(genArgs, "transparentBackground", item, "transparentBackground")
		args = genArgs
		failure := item["failure"]
		if failure != nil {
			result = failure
			failed = true
		} else if savedPath, hasSaved := item["savedPath"]; hasSaved && truthy(savedPath) {
			result = nativejson.NewObject(nativejson.Property{Name: "savedPath", Value: savedPath})
		}
	default:
		return nil, false
	}
	status, _ := nativejson.AsText(item["status"])
	state := ""
	switch {
	case failed || status == "failed" || status == "declined":
		state = "failed"
	case status == "interrupted":
		state = "stopped"
	case status == "inProgress" || status == "in_progress":
		state = "running"
	case status == "completed" ||
		(typeName == "webSearch" && item["results"] != nil) ||
		(typeName == "imageView" && isNonEmptyString(item["path"])):
		state = "succeeded"
	}
	activity := &protocol.Activity{
		Type:       "tool",
		ToolName:   toolName,
		Arguments:  encoded(args),
		Result:     encoded(result),
		State:      state,
		DurationMs: optionalDuration(item["durationMs"]),
	}
	if typeName == "imageView" {
		if path, isStr := nativejson.AsText(item["path"]); isStr {
			activity.ImagePath = protocol.Known(path)
		}
	}
	return activity, true
}

func optionalDuration(value any) protocol.Optional[float64] {
	if ms, ok := durationMilliseconds(value); ok {
		return protocol.Known(ms)
	}
	return protocol.Optional[float64]{}
}

func isFalse(v any) bool {
	b, ok := v.(bool)
	return ok && !b
}

func isNonEmptyString(v any) bool {
	s, ok := nativejson.AsText(v)
	return ok && s != ""
}

func truthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case float64:
		return value != 0 && !math.IsNaN(value)
	case nativejson.SurrogateText:
		return value != ""
	case string:
		return value != ""
	default:
		return true
	}
}

// jsCoalesce renders `${item.x ?? fallback}` for string interpolation: only
// null/undefined take the fallback; other values coerce to JS strings.
func jsCoalesce(v any, fallback string) string {
	switch value := v.(type) {
	case nil:
		return fallback
	case nativejson.SurrogateText:
		return string(value)
	case string:
		return value
	case bool:
		if value {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberString(value)
	case []any:
		parts := make([]string, len(value))
		for i, part := range value {
			parts[i] = jsCoalesce(part, "")
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// jsNumberString approximates ECMAScript Number::toString(radix=10) for the
// interpolation fallback. Integers below 1e21 use fixed notation (matching
// JS), larger or fractional values use Go's shortest form with the exponent
// normalized to the JS style (e.g. 1e-7, not 1e-07).
func jsNumberString(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "Infinity"
	}
	if math.IsInf(v, -1) {
		return "-Infinity"
	}
	if v == 0 {
		// JS stringifies -0 as "0".
		return "0"
	}
	if math.Abs(v) >= 1e-6 && math.Abs(v) < 1e21 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	s := strconv.FormatFloat(v, 'e', -1, 64)
	// Go: "1.5e+21"/"1e-07"; JS: "1.5e+21"/"1e-7". Trim exponent zeros.
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		mantissa := s[:i]
		exponent := s[i+1:]
		sign := "+"
		if exponent[0] == '+' || exponent[0] == '-' {
			sign = string(exponent[0])
			exponent = exponent[1:]
		}
		for len(exponent) > 1 && exponent[0] == '0' {
			exponent = exponent[1:]
		}
		if strings.HasSuffix(mantissa, ".0") {
			mantissa = mantissa[:len(mantissa)-2]
		}
		return mantissa + "e" + sign + exponent
	}
	return s
}

var turnErrorControl = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")

// turnErrorMessage mirrors the TS turnError text pipeline: typeof check on
// error?.message, control character strip (keeping \t \n \r), trim, then a
// 8192 UTF-16 unit cap in that order.
func turnErrorMessage(turnErr any) (string, bool) {
	raw, isMap := asMap(turnErr)
	if !isMap {
		return "", false
	}
	text, isStr := nativejson.AsText(raw["message"])
	if !isStr {
		return "", false
	}
	text = turnErrorControl.ReplaceAllString(text, "")
	text = trimJSWhitespace(text)
	return toolpreview.SliceUTF16(text, 8192), true
}

// NativeTurn is the normalized turn record ProjectMessages consumes.
type NativeTurn struct {
	ID        string
	Items     []map[string]any
	StartedAt *float64
	Status    any
	Error     any
}

// ProjectMessages mirrors TS projectMessages: newest turn first, per-item
// projection with an optional activity channel, and a terminal error message
// appended for failed turns.
func ProjectMessages(turns []NativeTurn, includeActivity bool) []protocol.Message {
	messages := make([]protocol.Message, 0, len(turns))
	// [...turns].reverse(): iterate a reversed copy, originals untouched.
	for i := len(turns) - 1; i >= 0; i-- {
		turn := turns[i]
		for _, item := range turn.Items {
			var activity *protocol.Activity
			var text string
			typeName, _ := nativejson.AsText(item["type"])
			switch {
			case typeName == "userMessage":
				content, exists := item["content"]
				if !exists || content == nil {
					content = []any{}
				}
				text = inputText(content)
			case typeName == "agentMessage":
				text, _ = nativejson.AsText(item["text"]) // item.text ?? ''
			case includeActivity && typeName == "reasoning":
				summaryParts, _ := item["summary"].([]any)
				kept := make([]string, 0, len(summaryParts))
				for _, part := range summaryParts {
					if s, isStr := nativejson.AsText(part); isStr && trimJSWhitespace(s) != "" {
						kept = append(kept, s)
					}
				}
				text = strings.Join(kept, "\n\n")
				if text == "" {
					continue
				}
				activity = &protocol.Activity{Type: "thought", DurationMs: optionalDuration(item["durationMs"])}
			case includeActivity:
				if act, ok := toolActivity(item); ok {
					activity = act
					text = ""
				} else {
					continue
				}
			default:
				continue
			}
			role := "assistant"
			if typeName == "userMessage" {
				role = "user"
			}
			timestamp := 0.0
			if turn.StartedAt != nil {
				timestamp = *turn.StartedAt
			}
			messages = append(messages, protocol.Message{
				MessageIdentity: protocol.MessageIdentity{
					ID:        identityString(item["id"]),
					TurnID:    turn.ID,
					ClientID:  stringPointer(item["clientId"]),
					Role:      role,
					Timestamp: protocol.Number(timestamp * 1000),
				},
				Text:     protocol.Text(text),
				Activity: activity,
			})
		}
		messages = append(messages, turnError(turn)...)
	}
	return messages
}

// identityString passes native ids through; native ids are JSON strings and
// non-string shapes have no TS-visible projection anyway.
func identityString(v any) string {
	if s, ok := nativejson.AsText(v); ok {
		return s
	}
	return ""
}

func stringPointer(v any) *string {
	if s, ok := nativejson.AsText(v); ok {
		return &s
	}
	return nil
}

func turnError(turn NativeTurn) []protocol.Message {
	if status, _ := nativejson.AsText(turn.Status); status != "failed" {
		return nil
	}
	text, ok := turnErrorMessage(turn.Error)
	if !ok || text == "" {
		text = "Codex failed to complete this turn."
	}
	timestamp := 0.0
	if turn.StartedAt != nil {
		timestamp = *turn.StartedAt
	}
	return []protocol.Message{{
		MessageIdentity: protocol.MessageIdentity{
			ID:        "filo-turn-error:" + turn.ID,
			TurnID:    turn.ID,
			ClientID:  nil,
			Role:      "assistant",
			Timestamp: protocol.Number(timestamp * 1000),
			Error:     protocol.Known(true),
		},
		Text: protocol.Text(text),
	}}
}

func trimJSWhitespace(value string) string {
	return strings.Trim(value, "\u0009\u000a\u000b\u000c\u000d\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}
