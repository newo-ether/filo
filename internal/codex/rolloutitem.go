package codex

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/newo-ether/filo/internal/nativejson"
)

// RolloutItem maps a completed original event onto the same native item shape
// as the indexed reader. Public summaries survive; raw reasoning is discarded.
func RolloutItem(value any) (map[string]any, error) {
	source, ok := asMap(value)
	if !ok {
		return nil, nil
	}
	if _, ok := nativejson.AsText(source["id"]); !ok {
		return nil, nil
	}
	typeName, ok := nativejson.AsText(source["type"])
	if !ok {
		return nil, nil
	}
	if typeName == "" {
		return nil, errors.New("Invalid native item type")
	}
	first, size := utf8.DecodeRuneInString(typeName)
	typeName = string(unicode.ToLower(first)) + typeName[size:]
	item := make(map[string]any, len(source))
	for key, value := range source {
		item[key] = value
	}
	item["type"] = typeName
	content := make([]any, 0)
	if parts, ok := source["content"].([]any); ok {
		for _, part := range parts {
			fields, _ := asMap(part)
			if kind, _ := nativejson.AsText(fields["type"]); kind == "Text" {
				content = append(content, map[string]any{"type": "text", "text": fields["text"]})
			} else {
				content = append(content, part)
			}
		}
	}
	item["content"] = content
	if typeName == "agentMessage" {
		parts := make([]string, 0)
		for _, part := range content {
			fields, _ := asMap(part)
			if kind, _ := nativejson.AsText(fields["type"]); kind == "text" {
				parts = append(parts, jsCoalesce(fields["text"], ""))
			}
		}
		item["text"] = nativejson.SurrogateText(strings.Join(parts, "\n"))
	}
	delete(item, "summary")
	if summary, ok := source["summary_text"].([]any); ok {
		item["summary"] = summary
	}
	delete(item, "raw_content")
	for target, alternate := range map[string]string{
		"clientId": "client_id", "contentItems": "content_items", "receiverThreadIds": "receiver_thread_ids",
		"agentsStates": "agents_states", "aggregatedOutput": "aggregated_output", "exitCode": "exit_code",
	} {
		if source[alternate] != nil {
			item[target] = source[alternate]
		}
	}
	if command, ok := source["command"].([]any); ok {
		parts := make([]string, len(command))
		for i, value := range command {
			parts[i] = jsCoalesce(value, "")
		}
		item["command"] = nativejson.SurrogateText(strings.Join(parts, " "))
	}
	delete(item, "durationMs")
	duration, _ := asMap(source["duration"])
	seconds, secondsOK := duration["secs"].(float64)
	nanos, nanosOK := duration["nanos"].(float64)
	if secondsOK && nanosOK && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && !math.IsNaN(nanos) && !math.IsInf(nanos, 0) {
		item["durationMs"] = seconds*1000 + nanos/1e6
	}
	return item, nil
}
