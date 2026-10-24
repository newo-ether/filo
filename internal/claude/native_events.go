package claude

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func (s *NativeSession) handle(event map[string]json.RawMessage) error {
	kind := stringField(event, "type")
	if kind == "control_request" {
		// Filo has no interactive approval surface. Do not bypass native policy.
		return s.write(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": stringField(event, "request_id"),
			"response": map[string]any{"behavior": "deny", "message": "Interactive approval is unavailable in this Filo session"}}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "control_response" {
		var response struct {
			Subtype   string                     `json:"subtype"`
			RequestID string                     `json:"request_id"`
			Response  map[string]json.RawMessage `json:"response"`
		}
		if json.Unmarshal(event["response"], &response) != nil {
			return errors.New("Invalid Claude control response")
		}
		if c := s.controls[response.RequestID]; c != nil {
			delete(s.controls, response.RequestID)
			if response.Subtype != "success" {
				c.err = errors.New("Claude native control was rejected")
			}
			c.data = response.Response
			close(c.done)
		}
		return nil
	}
	id := stringField(event, "session_id")
	if id != "" {
		if !nativeUUID.MatchString(id) || s.id != "" && s.id != id {
			return errors.New("Claude native session identity changed")
		}
		s.id = id
	}
	if id == "" && (kind == "user" || kind == "assistant" || kind == "result" || kind == "stream_event") {
		return errors.New("Claude event has no native session identity")
	}
	switch kind {
	case "system":
		if stringField(event, "subtype") == "init" {
			if model := stringField(event, "model"); validNativeModel(model) {
				s.model = model
			}
		}
	case "stream_event":
		if err := s.streamEvent(event); err != nil {
			return err
		}
	case "user":
		if string(event["isReplay"]) != "true" {
			s.toolResults(event)
			break
		}
		input := s.active
		if input == nil || input.uuid != "" {
			return errors.New("Unexpected Claude input replay")
		}
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(event["message"], &message) != nil || message.Role != "user" || nativeText(message.Content) != input.text {
			return errors.New("Claude input replay does not match pending admission")
		}
		uuid := stringField(event, "uuid")
		if !nativeUUID.MatchString(uuid) || s.id == "" {
			return errors.New("Claude input replay has no native identity")
		}
		input.uuid = uuid
		s.receipts = append(s.receipts, protocol.InputReceipt{ClientID: input.client, TurnID: input.turn, MessageID: uuid})
		if len(s.receipts) > 128 {
			s.receipts = s.receipts[len(s.receipts)-128:]
		}
		client := input.client
		s.appendRow(protocol.Message{MessageIdentity: protocol.MessageIdentity{ID: uuid, NativeID: uuid, TurnID: input.turn, GroupID: uuid, ClientID: &client, Role: "user", Timestamp: nativeTimestamp(event)}, Text: protocol.Text(input.text)})
		close(input.done)
	case "assistant":
		if err := s.assistantBlocks(event); err != nil {
			return err
		}
	case "result":
		input := s.active
		if input == nil || input.uuid == "" || !nativeUUID.MatchString(stringField(event, "uuid")) || id != s.id {
			return errors.New("Unmatched Claude terminal evidence")
		}
		s.finishActivities(stringField(event, "terminal_reason"))
		input.resultID = stringField(event, "uuid")
		s.completed = input.turn
		s.active = nil
		close(input.terminal)
	default:
		return nil
	}
	s.signalLocked()
	// Notification must not run under the session mutex.
	if s.options.OnChange != nil {
		go s.notify()
	}
	return nil
}

func nativeText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}
	return result.String()
}

func nativeTimestamp(event map[string]json.RawMessage) protocol.Number {
	stamp, err := time.Parse(time.RFC3339Nano, stringField(event, "timestamp"))
	if err != nil {
		return protocol.Number(math.NaN())
	}
	return protocol.Number(stamp.UnixMilli())
}

func (s *NativeSession) appendRow(row protocol.Message) {
	s.putNativeRow(row)
}
