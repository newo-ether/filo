package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

const rolloutPrefix = "filo.rollout.items."
const IndexedHistoryCursor = "filo.rollout.indexed"
const maxNativeRecordBytes = 256 * 1024 * 1024

var eventHead = regexp.MustCompile(`"type"\s*:\s*"event_msg"`)
var completedHead = regexp.MustCompile(`"type"\s*:\s*"item_completed"`)

type rolloutCursor struct {
	ID            string  `json:"id"`
	File          string  `json:"file"`
	Before        int64   `json:"before"`
	Anchor        string  `json:"anchor"`
	IndexedCursor *string `json:"indexedCursor,omitempty"`
}

type MessagePage struct {
	Messages   []protocol.Message
	NextCursor *string
}

func IsRolloutCursor(cursor string) bool { return strings.HasPrefix(cursor, rolloutPrefix) }

func ReadRolloutPage(ctx context.Context, path, id, cursor, anchor string, activity bool, excludedTurns []string, indexedCursor string) (*MessagePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if indexedCursor == "" {
		indexedCursor = IndexedHistoryCursor
	}
	hash := sha256.Sum256([]byte(path))
	key := hex.EncodeToString(hash[:])
	var continued *rolloutCursor
	if IsRolloutCursor(cursor) {
		if len(cursor) > 2048 {
			return nil, errors.New("Invalid native history cursor")
		}
		body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, rolloutPrefix))
		if err != nil {
			return nil, errors.New("Invalid native history cursor")
		}
		value, _, err := nativejson.Read(bytes.NewReader(body), false)
		if err != nil {
			return nil, errors.New("Invalid native history cursor")
		}
		fields, _ := nativejson.Fields(value)
		cursorID, idOK := nativejson.AsText(fields["id"])
		fileKey, fileOK := nativejson.AsText(fields["file"])
		cursorAnchor, anchorOK := nativejson.AsText(fields["anchor"])
		before, beforeOK := fields["before"].(float64)
		if !idOK || !fileOK || !anchorOK || cursorID != id || fileKey != key || !beforeOK || math.Trunc(before) != before || before < 0 || before > 9007199254740991 {
			return nil, errors.New("Invalid native history cursor")
		}
		continued = &rolloutCursor{ID: cursorID, File: fileKey, Before: int64(before), Anchor: cursorAnchor}
		if raw, exists := fields["indexedCursor"]; exists {
			next, ok := nativejson.AsText(raw)
			if !ok || len(next) > 1024 {
				return nil, errors.New("Invalid index cursor")
			}
			continued.IndexedCursor = &next
		}
		anchor = continued.Anchor
		if continued.IndexedCursor != nil {
			indexedCursor = *continued.IndexedCursor
		} else {
			indexedCursor = IndexedHistoryCursor
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if continued != nil && continued.Before > info.Size() {
		return nil, errors.New("Native history was truncated")
	}
	header := make([]byte, min(int64(scanBytes), info.Size()))
	n, err := file.ReadAt(header, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	first := bytes.IndexByte(header[:n], 10)
	if first < 0 {
		return nil, errors.New("Missing native history identity")
	}
	identity, _, err := nativejson.Read(bytes.NewReader(header[:first]), false)
	if err != nil {
		return nil, err
	}
	fields, _ := nativejson.Fields(identity)
	payload, _ := nativejson.Fields(fields["payload"])
	if nativejson.Text(fields["type"]) != "session_meta" || nativejson.Text(payload["id"]) != id {
		return nil, errors.New("Native history identity mismatch")
	}
	page := &MessagePage{Messages: make([]protocol.Message, 0)}
	seen := make(map[string]bool)
	scanned, before, found := 0, info.Size(), false
	if continued != nil {
		before = continued.Before
	}
	for span, err := range records(ctx, file, before) {
		if err != nil {
			return nil, err
		}
		before = span.start
		head := make([]byte, min(int64(1024), span.end-span.start))
		n, err := file.ReadAt(head, span.start)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if eventHead.Match(head[:n]) && completedHead.Match(head[:n]) {
			if span.end-span.start > maxNativeRecordBytes {
				return nil, errors.New("Native event exceeds its read bound")
			}
			value, _, err := nativejson.Read(contextReader{ctx, io.NewSectionReader(file, span.start, span.end-span.start)}, true)
			if err != nil {
				return nil, err
			}
			event, _ := nativejson.Fields(value)
			payload, _ := nativejson.Fields(event["payload"])
			threadID, _ := nativejson.AsText(payload["thread_id"])
			turnID, validTurn := nativejson.AsText(payload["turn_id"])
			if threadID != id || !validTurn {
				return nil, errors.New("Native event identity mismatch")
			}
			item, err := codex.RolloutItem(payload["item"])
			if err != nil {
				return nil, err
			}
			itemID := nativejson.Text(item["id"])
			if item != nil && itemID == anchor {
				page.NextCursor = &indexedCursor
				slices.Reverse(page.Messages)
				return page, nil
			}
			found = true
			if item != nil && !seen[itemID] && !slices.Contains(excludedTurns, turnID) {
				seen[itemID] = true
				started := eventStarted(payload, event)
				page.Messages = append(page.Messages, codex.ProjectMessages([]codex.NativeTurn{{ID: turnID, Items: []map[string]any{item}, StartedAt: &started}}, activity)...)
			}
		}
		scanned++
		if len(page.Messages) >= 16 || scanned >= 512 {
			if !found && continued == nil {
				return nil, nil
			}
			body, _ := json.Marshal(rolloutCursor{id, key, before, anchor, &indexedCursor})
			next := rolloutPrefix + base64.RawURLEncoding.EncodeToString(body)
			page.NextCursor = &next
			slices.Reverse(page.Messages)
			return page, nil
		}
	}
	if !found && continued == nil {
		return nil, nil
	}
	slices.Reverse(page.Messages)
	return page, nil
}

func eventStarted(payload, event map[string]any) float64 {
	if value, ok := payload["started_at_ms"].(float64); ok && !math.IsNaN(value) && !math.IsInf(value, 0) {
		return value / 1000
	}
	timestamp, err := time.Parse(time.RFC3339Nano, nativejson.Text(event["timestamp"]))
	if err != nil {
		return math.NaN()
	}
	return float64(timestamp.UnixMilli()) / 1000
}
