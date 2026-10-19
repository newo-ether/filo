package service

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

// MessageNode derives transport sizing and cached image/payload identity from
// the message projection. Nil serialized hashes the service's wire encoding.
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
	// An absent image list stays absent; an empty list reports zero.
	if message.ImageLinks != nil {
		node.ImageCount = protocol.Known(len(message.ImageLinks))
	}
	if message.Activity != nil {
		// Present activities always report hasImage. Only native imageView
		// references count; the private path never leaves this projection.
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

// hasContent recognizes user turns, tool activity, text beyond JavaScript
// whitespace and explicit continuation markers.
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
