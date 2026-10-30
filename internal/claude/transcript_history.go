package claude

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

const transcriptCursorPrefix = "claude.transcript."

// Transcript pages precede registration. Native timestamps define this read
// boundary only; they never assign a message to a native turn.
type transcriptCursor struct {
	Scope, Origin, Session, Binding, Path string
	Before, Cutoff, Through               int64
	Anchor, ThroughAnchor                 string
	Live                                  bool
}

type transcriptSnapshot struct {
	path, nonce, origin string
	registered          time.Time
}

type transcriptPage struct {
	protocol.ConversationPage
	includeLive bool
}

func (a *Adapter) readTranscript(ctx context.Context, id, cursor string, activity bool, snapshot transcriptSnapshot) (transcriptPage, error) {
	page := transcriptPage{ConversationPage: protocol.ConversationPage{Messages: []protocol.Message{}}}
	path, err := scopedTranscriptPath(snapshot.path, id)
	if err != nil {
		return page, err
	}
	file, err := os.Open(path)
	if err != nil {
		return page, errors.New("Claude native transcript is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return page, errors.New("Claude native transcript is not a regular file")
	}
	digest := sha256.Sum256([]byte(path))
	state := transcriptCursor{Scope: a.scope, Origin: snapshot.origin, Session: id, Binding: snapshot.nonce,
		Path: hex.EncodeToString(digest[:]), Before: info.Size(), Through: info.Size(),
		ThroughAnchor: transcriptAnchor(file, info.Size()), Cutoff: snapshot.registered.UnixMilli(), Live: true}
	if cursor != "" {
		decoded, err := a.decodeTranscriptCursor(cursor)
		if err != nil || decoded.Scope != state.Scope || decoded.Origin != state.Origin || decoded.Session != id ||
			decoded.Binding != state.Binding || decoded.Path != state.Path || decoded.Cutoff != state.Cutoff ||
			decoded.Before < 0 || decoded.Before > decoded.Through || decoded.Through > info.Size() ||
			decoded.Anchor != transcriptAnchor(file, decoded.Before) || decoded.ThroughAnchor != transcriptAnchor(file, decoded.Through) {
			return page, errors.New("Claude native transcript cursor or binding changed")
		}
		state = decoded
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	page.includeLive = state.Live
	boundary := state
	boundary.Anchor = transcriptAnchor(file, boundary.Before)
	page.PageCursor = protocol.Known(a.encodeTranscriptCursor(boundary))
	start := max(int64(0), state.Before-maxSessionBytes)
	data := make([]byte, state.Before-start)
	if _, err := file.ReadAt(data, start); err != nil && err != io.EOF {
		return page, errors.New("Claude native transcript read failed")
	}
	// The first partial record is left for the next page. Native writes may also
	// leave a final partial line; that line cannot be projected as a complete row.
	first := 0
	if start > 0 {
		first = bytes.IndexByte(data, '\n') + 1
		if first == 0 || first == len(data) {
			return page, errors.New("Claude native transcript record exceeds the read bound")
		}
	}
	end := len(data)
	if end > 0 && data[end-1] != '\n' {
		end = bytes.LastIndexByte(data, '\n') + 1
	}
	tools := map[string]*transcriptTool{}
	if activity {
		// Read at most one additional window toward the fixed snapshot end.
		// Results never cross registration or observe a later append on replay.
		through := min(state.Through, state.Before+maxSessionBytes)
		later := make([]byte, through-state.Before)
		if _, err := file.ReadAt(later, state.Before); err != nil && err != io.EOF {
			return page, errors.New("Claude native tool result read failed")
		}
		if err := indexTranscriptTools(ctx, tools, data[first:end], id, snapshot.origin, state.Cutoff); err != nil {
			return page, err
		}
		if err := indexTranscriptTools(ctx, tools, later, id, snapshot.origin, state.Cutoff); err != nil {
			return page, err
		}
	}
	before := start + int64(first)
	pageBytes := 0
	seen := make(map[string]bool)
	for end > first {
		if err := ctx.Err(); err != nil {
			return transcriptPage{}, err
		}
		lineEnd := end - 1
		lineStart := bytes.LastIndexByte(data[first:lineEnd], '\n') + first + 1
		messages := transcriptMessages(data[lineStart:lineEnd], id, snapshot.origin, state.Cutoff, activity, tools)
		encoded, _ := json.Marshal(messages)
		if len(messages) > maxRows || len(encoded) > 3<<20 {
			return page, errors.New("Claude native transcript row exceeds the page bound")
		}
		if len(page.Messages)+len(messages) > maxRows || pageBytes+len(encoded) > 3<<20 {
			before = start + int64(end)
			break
		}
		for i := len(messages) - 1; i >= 0; i-- {
			if !seen[messages[i].ID] {
				seen[messages[i].ID] = true
				page.Messages = append(page.Messages, messages[i])
			}
		}
		pageBytes += len(encoded)
		end = lineStart
	}
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	if before > 0 {
		state.Live = false
		state.Before = before
		state.Anchor = transcriptAnchor(file, before)
		next := a.encodeTranscriptCursor(state)
		page.NextCursor = &next
	}
	return page, ctx.Err()
}

func scopedTranscriptPath(path, id string) (string, error) {
	invalid := errors.New("Claude native transcript path is outside the selected native store")
	if !filepath.IsAbs(path) || filepath.Base(path) != id+".jsonl" {
		return "", invalid
	}
	home := os.Getenv("CLAUDE_CONFIG_DIR")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", invalid
		}
		home = filepath.Join(user, ".claude")
	}
	root, err := filepath.EvalSymlinks(filepath.Join(home, "projects"))
	if err != nil {
		return "", invalid
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", invalid
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", invalid
	}
	return resolved, nil
}

func transcriptAnchor(file *os.File, before int64) string {
	data := make([]byte, min(before, 256))
	if _, err := file.ReadAt(data, before-int64(len(data))); err != nil && err != io.EOF {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (a *Adapter) encodeTranscriptCursor(state transcriptCursor) string {
	body, _ := json.Marshal(state)
	mac := hmac.New(sha256.New, []byte(a.token))
	mac.Write(body)
	return transcriptCursorPrefix + base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Adapter) decodeTranscriptCursor(cursor string) (transcriptCursor, error) {
	var state transcriptCursor
	parts := strings.Split(strings.TrimPrefix(cursor, transcriptCursorPrefix), ".")
	if !strings.HasPrefix(cursor, transcriptCursorPrefix) || len(cursor) > 4096 || len(parts) != 2 {
		return state, errors.New("Invalid Claude transcript cursor")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	signature, sigErr := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, []byte(a.token))
	mac.Write(body)
	if err != nil || sigErr != nil || !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(body, &state) != nil {
		return state, errors.New("Invalid Claude transcript cursor")
	}
	return state, nil
}

type transcriptRecord struct {
	Type, UUID, SessionID, Entrypoint, Timestamp string
	IsSidechain, IsMeta                          bool
	ParentToolUseID                              string `json:"parent_tool_use_id"`
	Message                                      struct {
		Role    string
		Content json.RawMessage
	}
	blocks []nativeContentBlock
	stamp  time.Time
}

func parseTranscriptRecord(line []byte, id, origin string, cutoff int64) (transcriptRecord, bool) {
	var row transcriptRecord
	if len(line) > 2<<20 || json.Unmarshal(line, &row) != nil || row.SessionID != id || !nativeUUID.MatchString(row.UUID) ||
		row.IsSidechain || row.IsMeta || row.ParentToolUseID != "" || row.Type != "user" && row.Type != "assistant" ||
		row.Entrypoint != "" && (origin == "terminal" && row.Entrypoint != "cli" || origin == "desktop" && row.Entrypoint != "claude-desktop-3p") {
		return row, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil || stamp.UnixMilli() >= cutoff || row.Message.Role != row.Type {
		return row, false
	}
	row.stamp = stamp
	var text string
	if json.Unmarshal(row.Message.Content, &text) == nil {
		row.blocks = []nativeContentBlock{{Type: "text", Text: text}}
	} else if json.Unmarshal(row.Message.Content, &row.blocks) != nil || len(row.blocks) > maxRows {
		return row, false
	}
	return row, true
}

// Only qualified assistant calls create identities. A result can update exactly
// one earlier call; duplicate IDs/results leave no claimed result or completion.
// Raw fields remain bounded by the source windows and use the shared preview
// owner only when their call is actually projected.
type transcriptTool struct {
	call            string
	duplicateCall   bool
	result          *nativeContentBlock
	duplicateResult bool
}

func indexTranscriptTools(ctx context.Context, tools map[string]*transcriptTool, data []byte, id, origin string, cutoff int64) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		row, valid := parseTranscriptRecord(data[:end], id, origin, cutoff)
		data = data[end+1:]
		if !valid {
			continue
		}
		for index, block := range row.blocks {
			if row.Type == "assistant" && block.Type == "tool_use" && nativeIdentifier(block.ID) && block.Name != "" && len(block.Name) <= 256 {
				if previous := tools[block.ID]; previous != nil {
					previous.duplicateCall = true
				} else {
					tools[block.ID] = &transcriptTool{call: row.UUID + ":tool:" + strconv.Itoa(index)}
				}
			} else if row.Type == "user" && block.Type == "tool_result" && nativeIdentifier(block.ToolUseID) {
				if call := tools[block.ToolUseID]; call != nil {
					if call.result != nil || strings.HasPrefix(call.call, row.UUID+":") {
						call.duplicateResult = true
					} else {
						result := block
						call.result = &result
					}
				}
			}
		}
	}
	return nil
}

func transcriptMessages(line []byte, id, origin string, cutoff int64, activity bool, tools map[string]*transcriptTool) []protocol.Message {
	row, valid := parseTranscriptRecord(line, id, origin, cutoff)
	if !valid {
		return nil
	}
	messages := []protocol.Message{}
	var combined protocol.Text
	textMessage := false
	for index, block := range row.blocks {
		if activity && row.Type == "assistant" && block.Type == "tool_use" && nativeIdentifier(block.ID) && block.Name != "" && len(block.Name) <= 256 {
			call := tools[block.ID]
			if call == nil || call.duplicateCall || call.call != row.UUID+":tool:"+strconv.Itoa(index) {
				continue
			}
			detail := &protocol.Activity{Type: "tool", ToolName: block.Name}
			if len(block.Input) > 0 {
				preview, _ := toolpreview.ToolPreview(string(block.Input))
				detail.Arguments = protocol.Known(protocol.Text(preview))
			}
			if call != nil && call.result != nil && !call.duplicateResult {
				detail.State = "succeeded"
				if call.result.IsError {
					detail.State = "failed"
				}
				if len(call.result.Content) > 0 {
					preview, _ := toolpreview.ToolPreview(string(call.result.Content))
					detail.Result = protocol.Known(protocol.Text(preview))
				}
			}
			message := protocol.Message{MessageIdentity: protocol.MessageIdentity{
				ID: block.ID, NativeID: block.ID, GroupID: row.UUID, Role: row.Type,
				Timestamp: protocol.Number(row.stamp.UnixMilli())}, Activity: detail}
			if nativeRowBytes(message) <= 2<<20 {
				messages = append(messages, message)
			}
			continue
		}
		value := block.Text
		var detail *protocol.Activity
		if block.Type == "thinking" && activity {
			value = block.Thinking
			detail = &protocol.Activity{Type: "thought"}
		} else if block.Type != "text" {
			continue
		}
		if value == "" || len(value) > maxRowBytes {
			continue
		}
		if detail == nil {
			if textMessage {
				combined += "\n"
			}
			combined += protocol.Text(value)
			textMessage = true
			continue
		}
		messages = append(messages, protocol.Message{MessageIdentity: protocol.MessageIdentity{
			ID: row.UUID + ":thought:" + strconv.Itoa(index), NativeID: row.UUID, GroupID: row.UUID, Role: row.Type,
			Timestamp: protocol.Number(row.stamp.UnixMilli())}, Text: protocol.Text(value), Activity: detail})
	}
	if textMessage && len(combined) <= maxRowBytes {
		messages = append(messages, protocol.Message{MessageIdentity: protocol.MessageIdentity{
			ID: row.UUID, NativeID: row.UUID, GroupID: row.UUID, Role: row.Type,
			Timestamp: protocol.Number(row.stamp.UnixMilli())}, Text: combined})
	}
	return messages
}
