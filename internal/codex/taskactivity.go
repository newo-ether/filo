package codex

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

const (
	// taskActivityItems bounds how many live items one activity buffer keeps.
	taskActivityItems = 128
	// taskActivityChars bounds the characters one activity buffer keeps. Very
	// large text is read from native history once completed, so a live buffer is
	// never unbounded.
	taskActivityChars = 4 * 1024 * 1024
)

// taskActivityEntry is one retained live message plus its accounting. A
// completed entry is never replaced by a later delta, and its summary keeps the
// native paragraph separation of a reasoning item.
type taskActivityEntry struct {
	message  protocol.Message
	complete bool
	chars    int
	summary  []string
}

// TaskActivity keeps the bounded public activity of exactly one native task.
// Completed native history stays authoritative and independently paged, so this
// buffer only bridges the window between an accepted turn and the publication of
// its catalog row.
type TaskActivity struct {
	id       string
	maxItems int
	maxChars int
	turnID   string
	// timestamp is the native turn start in milliseconds, the timestamp every live
	// message of this turn carries.
	timestamp  float64
	entries    map[string]*taskActivityEntry
	order      []string
	chars      int
	overflowed bool
}

// NewTaskActivity builds one activity buffer with the native defaults.
func NewTaskActivity(id string) *TaskActivity {
	return NewTaskActivityWith(id, taskActivityItems, taskActivityChars)
}

// NewTaskActivityWith builds one activity buffer with explicit bounds. Unlike the
// TS default parameters, a bound is used exactly as given, so a test can pin an
// overflow without a four megabyte payload.
func NewTaskActivityWith(id string, maxItems, maxChars int) *TaskActivity {
	return &TaskActivity{
		id:       id,
		maxItems: maxItems,
		maxChars: maxChars,
		entries:  map[string]*taskActivityEntry{},
	}
}

// Receive folds one native notification into the live buffer. Only the exact
// admitted task and the current turn are observed; an older turn may not reopen
// this buffer, and an overflow stops it instead of publishing a truncated suffix
// as a new full message.
//
// Divergence from the TS `receive(event: RpcNotification)`: the method and the
// decoded params are passed separately, which is the Go form of one packet. A
// turn id that is present but not text is refused outright instead of being kept
// as a non-text identity, which is a malformed-payload difference only.
func (a *TaskActivity) Receive(method string, params any) {
	fields, ok := nativejson.Fields(params)
	if !ok {
		return
	}
	threadID, _ := nativejson.AsText(fields["threadId"])
	if threadID != a.id {
		return
	}
	turn, _ := nativejson.Fields(fields["turn"])
	turnID := ""
	if raw, present := fields["turnId"]; present && raw != nil {
		text, isText := raw.(string)
		if !isText || text == "" {
			return
		}
		turnID = text
	} else {
		turnID, _ = nativejson.AsText(turn["id"])
		if turnID == "" {
			return
		}
	}
	if turnID != a.turnID {
		// A later turn only opens this buffer through its own start event.
		if a.turnID != "" && method != "turn/started" {
			return
		}
		a.clear()
		a.turnID = turnID
		a.overflowed = false
		a.timestamp = taskActivitySeconds(turn["startedAt"]) * 1000
	}
	if a.overflowed {
		return
	}
	if method == "turn/completed" && taskActivityText(turn["status"]) == "failed" {
		started := a.timestamp / 1000
		projected := turnID
		if named, _ := nativejson.AsText(turn["id"]); named != "" {
			projected = named
		}
		for _, message := range ProjectMessages([]NativeTurn{{
			ID:        projected,
			StartedAt: &started,
			Status:    turn["status"],
			Error:     turn["error"],
		}}, true) {
			a.put(message, true, nil)
		}
	}
	if item, isItem := asMap(fields["item"]); isItem &&
		(method == "item/started" || method == "item/completed") {
		started := a.timestamp / 1000
		messages := ProjectMessages([]NativeTurn{{
			ID:        turnID,
			Items:     []map[string]any{item},
			StartedAt: &started,
		}}, true)
		if len(messages) > 0 {
			a.put(messages[0], method == "item/completed", taskActivitySummary(item["summary"]))
		}
		return
	}
	itemID, isItemID := nativejson.AsText(fields["itemId"])
	if !isItemID || itemID == "" {
		return
	}
	delta, isDelta := fields["delta"].(string)
	if !isDelta {
		return
	}
	current := a.entries[itemID]
	if current != nil && current.complete {
		return
	}
	thought := method == "item/reasoning/summaryTextDelta"
	if method != "item/agentMessage/delta" && !thought {
		return
	}
	message := protocol.Message{}
	if current != nil {
		message = current.message
	} else {
		var activity *protocol.Activity
		if thought {
			activity = &protocol.Activity{Type: "thought"}
		}
		message = protocol.Message{
			MessageIdentity: protocol.MessageIdentity{
				ID:        itemID,
				TurnID:    turnID,
				Role:      "assistant",
				Timestamp: protocol.Number(a.timestamp),
			},
			Activity: activity,
		}
	}
	var summary []string
	if thought {
		index := 0
		if raw, present := fields["summaryIndex"]; present && raw != nil {
			number, isNumber := raw.(float64)
			if !isNumber || !jsSafeInteger(number) {
				a.clear()
				a.overflowed = true
				return
			}
			index = int(number)
		}
		if index < 0 || index >= a.maxItems {
			a.clear()
			a.overflowed = true
			return
		}
		if current != nil && current.summary != nil {
			summary = append([]string(nil), current.summary...)
		}
		for len(summary) <= index {
			summary = append(summary, "")
		}
		summary[index] += delta
		message.Text = protocol.Text(taskActivitySummaryText(summary))
	} else {
		message.Text += protocol.Text(delta)
	}
	a.put(message, false, summary)
}

// put accounts one message against the buffer bounds and evicts the oldest
// entries until both bounds hold as long as nothing overflows.
func (a *TaskActivity) put(message protocol.Message, complete bool, summary []string) {
	chars := taskActivityCharsOf(message)
	previous := a.entries[message.ID]
	if previous != nil {
		a.chars -= previous.chars
	}
	if chars > a.maxChars || (summary != nil && len(summary) > a.maxItems) {
		a.clear()
		a.overflowed = true
		return
	}
	if previous == nil {
		a.order = append(a.order, message.ID)
	}
	a.entries[message.ID] = &taskActivityEntry{
		message:  message,
		complete: complete,
		chars:    chars,
		summary:  summary,
	}
	a.chars += chars
	for len(a.order) > a.maxItems || a.chars > a.maxChars {
		oldest := a.order[0]
		a.order = a.order[1:]
		a.chars -= a.entries[oldest].chars
		delete(a.entries, oldest)
	}
}

// clear drops every retained entry of the current turn.
func (a *TaskActivity) clear() {
	a.entries = map[string]*taskActivityEntry{}
	a.order = nil
	a.chars = 0
}

// Merge folds the live buffer of the current turn into one persisted page. The
// persisted page stays authoritative: a live block only replaces an incomplete
// one in place, and a completed live tail is appended after the last persisted
// anchor.
func (a *TaskActivity) Merge(messages []protocol.Message, currentTurnID *string,
	activity bool, preferPersisted bool) []protocol.Message {
	if a.turnID == "" || currentTurnID == nil || *currentTurnID != a.turnID {
		return messages
	}
	positions := make(map[string]int, len(a.order))
	for index, id := range a.order {
		positions[id] = index
	}
	anchor := -1
	present := make(map[string]bool, len(messages))
	currentTurnPersisted := false
	for _, message := range messages {
		if index, exists := positions[message.ID]; exists && index > anchor {
			anchor = index
		}
		present[message.ID] = true
		if message.TurnID == a.turnID {
			currentTurnPersisted = true
		}
	}
	merged := make([]protocol.Message, 0, len(messages)+len(a.order))
	for _, message := range messages {
		if entry := a.entries[message.ID]; entry != nil && !entry.complete && !preferPersisted {
			merged = append(merged, entry.message)
			continue
		}
		merged = append(merged, message)
	}
	for index, id := range a.order {
		if present[id] {
			continue
		}
		entry := a.entries[id]
		if entry.complete && currentTurnPersisted && !(anchor >= 0 && index > anchor) {
			continue
		}
		merged = append(merged, entry.message)
	}
	if activity {
		return merged
	}
	// The activity channel is an opt-in addition to the page, never a substitute.
	filtered := make([]protocol.Message, 0, len(merged))
	for _, message := range merged {
		if message.Activity == nil {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

// taskActivitySummaryText joins the non-blank paragraphs of one reasoning
// summary, keeping the native paragraph separation.
func taskActivitySummaryText(summary []string) string {
	parts := make([]string, 0, len(summary))
	for _, part := range summary {
		if TrimJSWhitespace(part) == "" {
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n\n")
}

// taskActivitySummary reads the native reasoning summary of one item.
func taskActivitySummary(value any) []string {
	parts, ok := value.([]any)
	if !ok {
		return nil
	}
	summary := make([]string, 0, len(parts))
	for _, part := range parts {
		text, _ := nativejson.AsText(part)
		summary = append(summary, text)
	}
	return summary
}

// taskActivityCharsOf counts the characters one message costs, the way the TS
// `JSON.stringify(message).length` does. The encoder keeps HTML characters
// unescaped so the count is the JavaScript one and the count is in UTF-16 units.
func taskActivityCharsOf(message protocol.Message) int {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(message); err != nil {
		return 0
	}
	return utf16Len(strings.TrimSuffix(buffer.String(), "\n"))
}

// taskActivityText reads one optional text field.
func taskActivityText(value any) string {
	text, _ := nativejson.AsText(value)
	return text
}

// taskActivitySeconds reads a native start time, where any non-number counts as
// the TS `?? 0` fallback.
func taskActivitySeconds(value any) float64 {
	number, ok := value.(float64)
	if !ok {
		return 0
	}
	return number
}
