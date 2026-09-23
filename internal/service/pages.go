package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

const (
	// MaxResponseBytes bounds one encoded response body.
	MaxResponseBytes = 1024 * 1024
	// MaxConversationPageBytes bounds one conversation page body, including the
	// metadata reservation the pager keeps free for the enclosing envelope.
	MaxConversationPageBytes = 256 * 1024
	maxMessages              = 128
	toolPreviewCharacters    = 8192
	partCharacters           = 16384
	reservedMetadataBytes    = 16384
	maxCursorUnits           = 8192
	maxAnchorUnits           = 512
)

// previewTruncationNote is appended to a bounded tool preview; the full output
// stays in Codex.
const previewTruncationNote = "\n[Filo preview truncated; full output remains in Codex.]"

// ConversationReader is the native read surface the conversation layer pages
// through. It mirrors the TS Reader type; excludedTurns is only meaningful for
// readers that do not exclude their own live turns.
type ConversationReader interface {
	Read(ctx context.Context, id, cursor string, activity bool, excludedTurns []string) (protocol.ConversationPage, error)
}

// PageOptions mirrors the trailing arguments of the TS readConversationPage.
// Layer names the history cursor namespace ('desktop' when empty).
type PageOptions struct {
	Activity        bool
	Layer           string
	ExcludedTurns   []string
	IncludeMetadata bool
}

// historyCursor is the decoded filo.page cursor payload. Null and absent both
// decode to nil, matching the TS nullish reads.
type historyCursor struct {
	ID      string  `json:"id"`
	Native  *string `json:"native"`
	Before  *string `json:"before"`
	Through *string `json:"through"`
}

// pageCursorPayload is the encoded cursor body; insertion order matches the TS
// object literal so cursors stay byte-comparable across the two services.
type pageCursorPayload struct {
	ID      string  `json:"id"`
	Native  *string `json:"native,omitempty"`
	Before  *string `json:"before,omitempty"`
	Through *string `json:"through,omitempty"`
}

func cursorPrefix(layer string) string {
	if layer == "" {
		layer = "desktop"
	}
	return "filo.page." + layer + "."
}

// ReadConversationPage pages by native message identity inside large native
// turns without changing stored history.
func ReadConversationPage(ctx context.Context, source ConversationReader, id, cursor string,
	options PageOptions) (protocol.ConversationPage, error) {
	var empty protocol.ConversationPage
	prefix := cursorPrefix(options.Layer)
	nativeCursor := cursor
	var before, through *string
	if strings.HasPrefix(cursor, prefix) {
		decoded, ok := decodeHistoryCursor(cursor, cursor[len(prefix):], id)
		if !ok {
			return empty, CodexRpcError("Invalid history cursor", -32602)
		}
		nativeCursor = ""
		if decoded.Native != nil {
			nativeCursor = *decoded.Native
		}
		before, through = decoded.Before, decoded.Through
	}
	page, err := source.Read(ctx, id, nativeCursor, options.Activity, options.ExcludedTurns)
	if err != nil {
		return empty, err
	}
	if len(options.ExcludedTurns) > 0 {
		excluded := make(map[string]bool, len(options.ExcludedTurns))
		for _, turn := range options.ExcludedTurns {
			excluded[turn] = true
		}
		kept := make([]protocol.Message, 0, len(page.Messages))
		for _, message := range page.Messages {
			if !excluded[message.TurnID] {
				kept = append(kept, message)
			}
		}
		page.Messages = kept
	}
	page.Messages = assignGroupIDs(page.Messages)
	if page.Runtime != nil {
		runtime := *page.Runtime
		runtime.ServiceTierKnown = protocol.Known(runtime.ServiceTier.Known)
		hasUserMessage := runtime.ActiveTurnHasUserMessage.Known && runtime.ActiveTurnHasUserMessage.Value
		if !hasUserMessage && runtime.ActiveTurnID != nil {
			for _, message := range page.Messages {
				if message.Role == "user" && message.TurnID == *runtime.ActiveTurnID {
					hasUserMessage = true
					break
				}
			}
		}
		runtime.ActiveTurnHasUserMessage = protocol.Known(hasUserMessage)
		page.Runtime = &runtime
	}
	// Long native text remains fully readable; only its transport is segmented.
	page.Messages = splitTextParts(page.Messages)
	anchorIndex := -1
	if before != nil || through != nil {
		anchor := before
		if anchor == nil {
			anchor = through
		}
		for index, message := range page.Messages {
			if message.ID == *anchor {
				anchorIndex = index
				break
			}
		}
		if anchorIndex < 0 {
			return empty, CodexRpcError("History changed; reopen this session before paging", -32600)
		}
	}
	end := len(page.Messages)
	switch {
	case through != nil:
		end = anchorIndex + 1
	case before != nil:
		end = anchorIndex
	}
	metadata := page
	metadata.Messages = []protocol.Message{}
	if options.IncludeMetadata {
		metadata.Nodes = []protocol.MessageNode{}
	}
	bytes := Size(metadata) + reservedMetadataBytes
	if bytes > MaxConversationPageBytes {
		return empty, errors.New("Conversation metadata exceeds the display limit")
	}
	messages := make([]protocol.Message, 0, maxMessages)
	var nodes []protocol.MessageNode
	if options.IncludeMetadata {
		nodes = make([]protocol.MessageNode, 0, maxMessages)
	}
	start := end
	for start > 0 && len(messages) < maxMessages {
		message := presentationMessage(page.Messages[start-1])
		messageBytes := Size(message) + 1
		var node *protocol.MessageNode
		if options.IncludeMetadata {
			projected := MessageNode(message, nil)
			node = &projected
			messageBytes += Size(projected) + 1
		}
		if bytes+messageBytes > MaxConversationPageBytes {
			if len(messages) == 0 {
				return empty, errors.New("A native message exceeds the display limit; view it in Codex")
			}
			break
		}
		messages = append(messages, message)
		if node != nil {
			nodes = append(nodes, *node)
		}
		bytes += messageBytes
		start--
	}
	slicesReverse(messages)
	slicesReverse(nodes)
	result := page
	result.Messages = messages
	result.NextCursor = page.NextCursor
	if start > 0 {
		encoded := encodePageCursor(prefix, pageCursorPayload{ID: id, Native: nativePointer(nativeCursor), Before: &messages[0].ID})
		result.NextCursor = &encoded
	}
	result.PageCursor = protocol.Optional[string]{}
	if len(messages) > 0 {
		encoded := encodePageCursor(prefix, pageCursorPayload{ID: id, Native: nativePointer(nativeCursor), Through: &messages[len(messages)-1].ID})
		result.PageCursor = protocol.Known(encoded)
	}
	if options.IncludeMetadata {
		result.Nodes = nodes
	}
	return result, nil
}

// EncodeResponse bounds one encoded response body.
func EncodeResponse(value any) (string, error) {
	text := string(Marshal(value))
	if len(text) > MaxResponseBytes {
		return "", errors.New("Filo response exceeds the display limit")
	}
	return text, nil
}

// nativePointer keeps an absent native cursor absent, so its cursor key stays
// dropped exactly as TS drops an undefined value.
func nativePointer(cursor string) *string {
	if cursor == "" {
		return nil
	}
	return &cursor
}

func decodeHistoryCursor(cursor, encoded, id string) (historyCursor, bool) {
	if toolpreview.UTF16Len(cursor) > maxCursorUnits {
		return historyCursor{}, false
	}
	decoded, ok := decodeBase64URL(encoded)
	if !ok {
		return historyCursor{}, false
	}
	var payload historyCursor
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return historyCursor{}, false
	}
	anchor := payload.Before
	if anchor == nil {
		anchor = payload.Through
	}
	switch {
	case payload.ID != id:
		return historyCursor{}, false
	case anchor == nil || *anchor == "" || toolpreview.UTF16Len(*anchor) > maxAnchorUnits:
		return historyCursor{}, false
	case payload.Before != nil && payload.Through != nil:
		return historyCursor{}, false
	}
	return payload, true
}

func encodePageCursor(prefix string, payload pageCursorPayload) string {
	return prefix + base64.RawURLEncoding.EncodeToString(Marshal(payload))
}

// decodeBase64URL mirrors the leniency of Node's base64url decoder for the
// alphabet and padding while still refusing a corrupt payload.
func decodeBase64URL(value string) ([]byte, bool) {
	normalized := strings.NewReplacer("+", "-", "/", "_").Replace(value)
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(normalized, "="))
	if err != nil {
		return nil, false
	}
	return decoded, true
}

// assignGroupIDs mirrors the TS fold: each turn keeps one group identity, a
// user message resets it, and an explicit native groupId is preserved. The
// unset cases are tracked separately from empty strings because `??` only
// substitutes for null and undefined.
func assignGroupIDs(messages []protocol.Message) []protocol.Message {
	projected := make([]protocol.Message, 0, len(messages))
	var turn, user, group string
	userSet, groupSet := false, false
	for _, message := range messages {
		if turn != message.TurnID {
			turn, user, group = message.TurnID, "", ""
			userSet, groupSet = false, false
		}
		if message.Role == "user" {
			user, userSet, group = message.ID, true, ""
			groupSet = false
			projected = append(projected, message)
			continue
		}
		if !groupSet {
			if message.GroupID != "" {
				group = message.GroupID
			} else {
				group = "remote-group:" + message.TurnID + ":" + groupUser(user, userSet)
			}
			groupSet = true
		}
		message.GroupID = group
		projected = append(projected, message)
	}
	return projected
}

func groupUser(user string, set bool) string {
	if set {
		return user
	}
	return "assistant"
}

// splitTextParts segments oversized text for transport only.
func splitTextParts(messages []protocol.Message) []protocol.Message {
	parts := make([]protocol.Message, 0, len(messages))
	for _, message := range messages {
		text := string(message.Text)
		length := toolpreview.UTF16Len(text)
		if length <= partCharacters {
			parts = append(parts, message)
			continue
		}
		for start := 0; start < length; {
			end := start + partCharacters
			if end > length {
				end = length
			}
			if end < length && splitsSurrogatePair(text, end) {
				end--
			}
			part := message
			if start > 0 {
				part.ID = "filo-part:" + message.ID + ":" + strconv.Itoa(start)
			}
			if message.NativeID != "" {
				part.NativeID = message.NativeID
			} else {
				part.NativeID = message.ID
			}
			offset := message.TextOffset
			if !offset.Known {
				offset = protocol.Known(0)
			}
			part.TextOffset = protocol.Known(offset.Value + start)
			continues := end < length
			if !continues && message.TextContinues.Known {
				continues = message.TextContinues.Value
			}
			part.TextContinues = protocol.Known(continues)
			part.Text = protocol.Text(textRangeUnits(text, start, end))
			parts = append(parts, part)
			start = end
		}
	}
	return parts
}

// textRangeUnits returns the UTF-16 unit range [start,end). Every boundary this
// pager produces already sits on a code point boundary, so both slices are
// literal byte prefixes and the second one can be trimmed by its byte length.
func textRangeUnits(value string, start, end int) string {
	head := toolpreview.SliceUTF16(value, end)
	return head[len(toolpreview.SliceUTF16(value, start)):]
}

// splitsSurrogatePair mirrors the TS guard: a cut between a high and a low
// surrogate moves back one unit so the pair stays whole.
func splitsSurrogatePair(value string, index int) bool {
	last, hasLast := unitAt(value, index-1)
	next, hasNext := unitAt(value, index)
	return hasLast && hasNext && last >= 0xD800 && last <= 0xDBFF && next >= 0xDC00 && next <= 0xDFFF
}

// unitAt mirrors String.prototype.charCodeAt over the WTF-8 string form,
// including preserved lone surrogates.
func unitAt(value string, index int) (uint16, bool) {
	units := 0
	for offset := 0; offset < len(value); {
		width, size := nextUnit(value, offset)
		if units == index {
			return codeUnit(value, offset, width)
		}
		if width == 2 && units+1 == index {
			return lowCodeUnit(value, offset)
		}
		units += width
		offset += size
	}
	return 0, false
}

func codeUnit(value string, offset, width int) (uint16, bool) {
	if width == 1 && isWTF8Unit(value, offset) {
		return uint16(value[offset]&0x0F)<<12 | uint16(value[offset+1]&0x3F)<<6 | uint16(value[offset+2]&0x3F), true
	}
	r, _ := utf8.DecodeRuneInString(value[offset:])
	if r > 0xFFFF {
		high, _ := utf16.EncodeRune(r)
		return uint16(high), true
	}
	return uint16(r), true
}

func lowCodeUnit(value string, offset int) (uint16, bool) {
	r, _ := utf8.DecodeRuneInString(value[offset:])
	_, low := utf16.EncodeRune(r)
	return uint16(low), true
}

// nextUnit counts UTF-16 code units of the WTF-8 string form, where a preserved
// lone surrogate spans three bytes and counts as one unit.
func nextUnit(value string, position int) (int, int) {
	if isWTF8Unit(value, position) {
		return 1, 3
	}
	r, size := utf8.DecodeRuneInString(value[position:])
	if r > 0xFFFF {
		return 2, size
	}
	return 1, size
}

func isWTF8Unit(value string, position int) bool {
	return position+2 < len(value) && value[position] == 0xED &&
		value[position+1] >= 0xA0 && value[position+1] <= 0xBF && value[position+2]&0xC0 == 0x80
}

// presentationMessage attaches parsed image links to an assistant answer and
// bounds tool payload previews. Stored history is never modified.
func presentationMessage(message protocol.Message) protocol.Message {
	if message.Role == "assistant" && message.Activity == nil && !(message.Error.Known && message.Error.Value) {
		if links := NativeImageLinks(string(message.Text)); len(links) > 0 {
			message.ImageLinks = links
			return message
		}
	}
	if message.Activity != nil && message.Activity.Type == "tool" {
		activity := *message.Activity
		activity.Arguments = preview(activity.Arguments)
		activity.Result = preview(activity.Result)
		message.Activity = &activity
	}
	return message
}

func preview(value protocol.Optional[protocol.Text]) protocol.Optional[protocol.Text] {
	if !value.Known {
		return protocol.Optional[protocol.Text]{}
	}
	text := string(value.Value)
	if toolpreview.UTF16Len(text) <= toolPreviewCharacters {
		return value
	}
	return protocol.Known(protocol.Text(toolpreview.SliceUTF16(text, toolPreviewCharacters) + previewTruncationNote))
}

// slicesReverse restores native order after a backwards window walk.
func slicesReverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
