package claude

import (
	"errors"
	"math"
	"strconv"

	"github.com/newo-ether/filo/internal/protocol"
)

// A block identity is supplied by turn.step, never inferred from append order.
// Native tool IDs also join tool.call results to their exact streamed call.
type modBlock struct {
	Step      int            `json:"step"`
	Index     int            `json:"index"`
	Type      string         `json:"type"`
	NativeID  string         `json:"nativeId,omitempty"`
	Text      protocol.Text  `json:"text,omitempty"`
	ToolName  string         `json:"toolName,omitempty"`
	Arguments *protocol.Text `json:"arguments,omitempty"`
	Result    *protocol.Text `json:"result,omitempty"`
	State     string         `json:"state,omitempty"`
}

func validateBlock(e modEvent) error {
	if e.Title != nil || e.TranscriptPath != "" || e.UUID != "" || e.Role != "" || e.Text != "" || e.ObservedCommand != "" || e.Aborted || e.Dropped ||
		e.CanInput != nil || e.CanSettings != nil || e.CanStop != nil || e.Model != nil || e.ModelOptions != nil || e.Effort != nil || e.ContextTokens != nil || e.ContextWindow != nil {
		return errors.New("Native block cannot include other event fields")
	}
	b := e.Block
	if b == nil || !opaque.MatchString(e.TurnID) || b.Step < 0 || b.Step > 1000000 || b.Index < 0 || b.Index >= maxRows ||
		len(b.Text) > maxRowBytes || len(b.ToolName) > 256 || b.Arguments != nil && len(*b.Arguments) > 384<<10 ||
		b.Result != nil && len(*b.Result) > 384<<10 {
		return errors.New("Invalid bounded native block")
	}
	if b.Type != "text" && b.Type != "thought" && b.Type != "tool" {
		return errors.New("Unsupported native block type")
	}
	if b.Type == "tool" && (!opaque.MatchString(b.NativeID) || b.ToolName == "") {
		return errors.New("Native tool identity and name required")
	}
	if b.Type != "tool" && (b.NativeID != "" || b.ToolName != "" || b.Arguments != nil || b.Result != nil) {
		return errors.New("Text block cannot include tool fields")
	}
	switch b.State {
	case "", "running", "succeeded", "failed", "stopped":
	default:
		return errors.New("Invalid native activity state")
	}
	return nil
}

func blockID(turn string, b *modBlock) string {
	if b.Type == "tool" {
		return "mod-tool:" + b.NativeID
	}
	return "mod-block:" + turn + ":" + strconv.Itoa(b.Step) + ":" + strconv.Itoa(b.Index)
}

func (a *Adapter) projectBlock(r *registration, e modEvent) error {
	b := e.Block
	id := blockID(e.TurnID, b)
	row := observedRow{UUID: id, Role: "assistant", Text: b.Text, TurnID: e.TurnID, NativeID: id}
	if b.Type != "text" {
		row.Activity = &protocol.Activity{Type: b.Type, State: b.State, ToolName: b.ToolName}
		if b.Arguments != nil {
			row.Activity.Arguments = protocol.Known(*b.Arguments)
		}
		if b.Result != nil {
			row.Activity.Result = protocol.Known(*b.Result)
		}
	}
	for i, old := range r.rows {
		if old.UUID != id {
			continue
		}
		if old.TurnID != row.TurnID || (old.Activity == nil) != (row.Activity == nil) ||
			old.Activity != nil && (old.Activity.Type != row.Activity.Type || old.Activity.ToolName != row.Activity.ToolName) {
			return errors.New("Native block identity changed")
		}
		r.rowBytes -= observedBytes(old)
		r.rows[i] = row
		r.rowBytes += observedBytes(row)
		trimObservedRows(r)
		return nil
	}
	r.rows = append(r.rows, row)
	r.rowBytes += observedBytes(row)
	trimObservedRows(r)
	return nil
}

func observedBytes(row observedRow) int {
	count := len(row.Text)
	if row.Activity != nil {
		count += len(row.Activity.Arguments.Value) + len(row.Activity.Result.Value)
	}
	return count
}

func trimObservedRows(r *registration) {
	for len(r.rows) > maxRows || r.rowBytes > maxSessionBytes {
		r.rowBytes -= observedBytes(r.rows[0])
		r.rows = r.rows[1:]
		r.dropped = true
	}
}

func finishObservedBlocks(r *registration, turn string, aborted bool) {
	for i, row := range r.rows {
		if row.TurnID != turn || row.Activity == nil || row.Activity.State != "running" {
			continue
		}
		activity := *row.Activity
		activity.State = ""
		if aborted {
			activity.State = "stopped"
		}
		r.rows[i].Activity = &activity
	}
}

func observedMessage(row observedRow) protocol.Message {
	return protocol.Message{MessageIdentity: protocol.MessageIdentity{
		ID: row.UUID, NativeID: row.NativeID, GroupID: row.UUID, TurnID: row.TurnID,
		Role: row.Role, Timestamp: protocol.Number(math.NaN()),
	}, Text: row.Text, Activity: row.Activity}
}
