package service

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

// MessageNode keeps transport sizing and cached image/payload identity on the
// same projection as the TS messageNode. Pass nil serialized to hash the
// message as this service would serialize it on the wire.
func MessageNode(message protocol.Message, serialized []byte) protocol.MessageNode {
	if serialized == nil {
		serialized = Marshal(message)
	}
	digest := sha256.Sum256(serialized)
	node := protocol.MessageNode{
		MessageIdentity: message.MessageIdentity,
		Revision:        hex.EncodeToString(digest[:]),
		TextLength:      toolpreview.UTF16Len(string(message.Text)),
		HasContent:      hasContent(message),
	}
	// TS: imageCount: message.imageLinks?.length, so an absent list stays
	// absent while an empty decoded list still reports zero.
	if message.ImageLinks != nil {
		node.ImageCount = protocol.Known(len(message.ImageLinks))
	}
	if message.Activity != nil {
		// TS always emits hasImage for a present activity; only a genuine
		// imageView reference counts, and the path itself never leaves.
		activity := &protocol.ActivityNode{
			Type:       message.Activity.Type,
			State:      message.Activity.State,
			DurationMs: message.Activity.DurationMs,
			HasImage:   protocol.Known(message.Activity.ImagePath.Value != ""),
		}
		node.Activity = activity
	}
	return node
}

// hasContent mirrors the TS truthiness chain: a user turn, a tool activity, any
// non-JavaScript-whitespace body text or an explicit continuation marker.
func hasContent(message protocol.Message) bool {
	if message.Role == "user" {
		return true
	}
	if message.Activity != nil && message.Activity.Type == "tool" {
		return true
	}
	if codex.TrimJSWhitespace(string(message.Text)) != "" {
		return true
	}
	return message.TextContinues.Known && message.TextContinues.Value
}
