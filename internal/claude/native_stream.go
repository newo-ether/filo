package claude

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/newo-ether/filo/internal/protocol"
)

// stream_event UUIDs identify notifications, not assistant messages. Display row
// IDs add a native block index; NativeID always remains the actual message/tool ID.
type nativeStreamMessage struct {
	id     string
	blocks map[int]*nativeStreamBlock
}

type nativeStreamBlock struct {
	kind       string
	rowID      string
	nativeID   string
	finalUUID  string
	closed     bool
	final      bool
	omitted    bool
	partialArg bool
}

type nativeContentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func nativeIdentifier(id string) bool {
	return id != "" && len(id) <= 256 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n\t")
}

func nativeNested(event map[string]json.RawMessage) bool {
	return stringField(event, "parent_tool_use_id") != ""
}

func (s *NativeSession) streamEvent(envelope map[string]json.RawMessage) error {
	if s.active == nil || s.active.uuid == "" || nativeNested(envelope) {
		return nil
	}
	var event struct {
		Type         string                     `json:"type"`
		Message      map[string]json.RawMessage `json:"message"`
		Index        *int                       `json:"index"`
		ContentBlock nativeContentBlock         `json:"content_block"`
		Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if json.Unmarshal(envelope["event"], &event) != nil {
		return errors.New("Invalid Claude stream event")
	}
	if event.Type == "message_start" {
		id := stringField(event.Message, "id")
		if !nativeIdentifier(id) {
			// An event UUID cannot substitute for a missing native message ID.
			s.stream = nil
			return nil
		}
		s.stream = &nativeStreamMessage{id: id, blocks: map[int]*nativeStreamBlock{}}
		return nil
	}
	if s.stream == nil || event.Index == nil || *event.Index < 0 || *event.Index >= maxRows {
		return nil
	}
	index := *event.Index
	if event.Type == "content_block_start" {
		if s.stream.blocks[index] != nil {
			return nil
		}
		block, row := s.newNativeBlock(s.stream.id, index, event.ContentBlock, envelope)
		if block != nil {
			s.stream.blocks[index] = block
			block.omitted = !s.putNativeRow(row)
		}
		return nil
	}
	block := s.stream.blocks[index]
	if block == nil || block.final || block.closed {
		return nil
	}
	row, exists := s.nativeRow(block.rowID)
	if event.Type == "content_block_stop" {
		block.closed = true
		if exists && row.Activity != nil && row.Activity.Type == "thought" {
			row.Activity.State = "succeeded"
			s.putNativeRow(row)
		}
		return nil
	}
	if event.Type != "content_block_delta" || block.omitted || !exists {
		return nil
	}
	switch {
	case block.kind == "text" && event.Delta.Type == "text_delta":
		row.Text += protocol.Text(event.Delta.Text)
	case block.kind == "thinking" && event.Delta.Type == "thinking_delta":
		row.Text += protocol.Text(event.Delta.Thinking)
	case block.kind == "tool_use" && event.Delta.Type == "input_json_delta":
		if !block.partialArg {
			row.Activity.Arguments = protocol.Known(protocol.Text(""))
			block.partialArg = true
		}
		row.Activity.Arguments.Value += protocol.Text(event.Delta.PartialJSON)
	default:
		// Signatures and redacted thinking carry no public text.
		return nil
	}
	block.omitted = !s.putNativeRow(row)
	return nil
}

func (s *NativeSession) newNativeBlock(messageID string, index int, content nativeContentBlock, event map[string]json.RawMessage) (*nativeStreamBlock, protocol.Message) {
	rowID := messageID + ":block:" + strconv.Itoa(index)
	nativeID := messageID
	var activity *protocol.Activity
	text := content.Text
	switch content.Type {
	case "text":
	case "thinking":
		text = content.Thinking
		activity = &protocol.Activity{Type: "thought", State: "running"}
	case "tool_use":
		if !nativeIdentifier(content.ID) || len(content.Name) > 256 {
			return nil, protocol.Message{}
		}
		rowID, nativeID = content.ID, content.ID
		text = ""
		activity = &protocol.Activity{Type: "tool", ToolName: content.Name, State: "running"}
		if len(content.Input) > 0 {
			activity.Arguments = protocol.Known(protocol.Text(content.Input))
		}
	default:
		return nil, protocol.Message{}
	}
	row := protocol.Message{MessageIdentity: protocol.MessageIdentity{ID: rowID, NativeID: nativeID, GroupID: messageID,
		TurnID: s.active.turn, Role: "assistant", Timestamp: nativeTimestamp(event)}, Text: protocol.Text(text), Activity: activity}
	return &nativeStreamBlock{kind: content.Type, rowID: rowID, nativeID: nativeID}, row
}

func (s *NativeSession) assistantBlocks(event map[string]json.RawMessage) error {
	if s.active == nil || s.active.uuid == "" || nativeNested(event) {
		return nil
	}
	var message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(event["message"], &message) != nil || !nativeUUID.MatchString(stringField(event, "uuid")) {
		return errors.New("Invalid Claude assistant message identity")
	}
	uuid := stringField(event, "uuid")
	if !nativeIdentifier(message.ID) {
		message.ID = uuid
	}
	var content []nativeContentBlock
	if json.Unmarshal(message.Content, &content) != nil {
		return errors.New("Invalid Claude assistant content blocks")
	}
	if len(content) > maxRows {
		return nil
	}
	for index, value := range content {
		block, row := s.newNativeBlock(message.ID, index, value, event)
		if block == nil {
			continue
		}
		if value.Type != "tool_use" {
			// Without a matching partial block, the completed native envelope
			// UUID is the only unambiguous identity for an individual block.
			row.ID = uuid
			if len(content) > 1 {
				row.ID += ":block:" + strconv.Itoa(index)
			}
		}
		if s.stream != nil && s.stream.id == message.ID {
			match := s.finalNativeBlock(index, len(content), uuid, value)
			if match != nil {
				block = match
				row.ID, row.NativeID = block.rowID, block.nativeID
			}
		}
		if previous, ok := s.nativeRow(row.ID); ok && previous.Activity != nil && previous.Activity.Type == "tool" && previous.Activity.Result.Known {
			row.Activity.Result, row.Activity.State = previous.Activity.Result, previous.Activity.State
		}
		if row.Activity != nil && row.Activity.Type == "thought" {
			row.Activity.State = "succeeded"
		}
		block.final, block.closed, block.finalUUID = true, true, uuid
		block.omitted = !s.putNativeRow(row)
	}
	return nil
}

// Claude emits complete assistant blocks individually as well as full messages.
// Match by the native message and block sequence, never by text similarity.
func (s *NativeSession) finalNativeBlock(index, count int, uuid string, content nativeContentBlock) *nativeStreamBlock {
	if count > 1 {
		if block := s.stream.blocks[index]; block != nil && block.kind == content.Type && (content.Type != "tool_use" || block.nativeID == content.ID) {
			return block
		}
		return nil
	}
	var selected *nativeStreamBlock
	for at := 0; at < maxRows; at++ {
		block := s.stream.blocks[at]
		if block == nil || block.kind != content.Type || content.Type == "tool_use" && block.nativeID != content.ID {
			continue
		}
		if block.finalUUID == uuid {
			return block
		}
		if !block.final && selected == nil {
			selected = block
		}
	}
	return selected
}

func (s *NativeSession) toolResults(event map[string]json.RawMessage) {
	if s.active == nil || s.active.uuid == "" || nativeNested(event) {
		return
	}
	var message struct {
		Content []nativeContentBlock `json:"content"`
	}
	if json.Unmarshal(event["message"], &message) != nil {
		return
	}
	for _, content := range message.Content {
		if content.Type != "tool_result" || !nativeIdentifier(content.ToolUseID) {
			continue
		}
		row, ok := s.nativeRow(content.ToolUseID)
		if !ok || row.TurnID != s.active.turn || row.Activity == nil || row.Activity.Type != "tool" {
			continue
		}
		row.Activity.State = "succeeded"
		if content.IsError {
			row.Activity.State = "failed"
		}
		if len(content.Content) > 0 {
			row.Activity.Result = protocol.Known(protocol.Text(content.Content))
		}
		s.putNativeRow(row)
	}
}

func (s *NativeSession) finishActivities(reason string) {
	for _, original := range nativeRowsSnapshot(s.rows) {
		if original.TurnID != s.active.turn || original.Activity == nil || original.Activity.State != "running" {
			continue
		}
		original.Activity.State = ""
		if reason == "aborted_streaming" || reason == "interrupted" {
			original.Activity.State = "stopped"
		}
		s.putNativeRow(original)
	}
	s.stream = nil
}
