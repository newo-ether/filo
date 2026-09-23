package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func nodeMessage(id string) protocol.Message {
	return protocol.Message{
		MessageIdentity: protocol.MessageIdentity{
			ID:        id,
			TurnID:    "turn",
			Role:      "assistant",
			Timestamp: protocol.Number(1000),
		},
	}
}

func TestMessageNodeRevisionHashesTheWireForm(t *testing.T) {
	message := nodeMessage("m1")
	node := MessageNode(message, nil)
	if len(node.Revision) != 64 || strings.ToLower(node.Revision) != node.Revision {
		t.Fatalf("revision %q is not 64 lowercase hex characters", node.Revision)
	}
	digest := sha256.Sum256(Marshal(message))
	if want := hex.EncodeToString(digest[:]); node.Revision != want {
		t.Fatalf("revision = %q, want %q", node.Revision, want)
	}
}

func TestMessageNodeRevisionHonoursAnExplicitSerialization(t *testing.T) {
	message := nodeMessage("m1")
	serialized := []byte("caf\u00e9")
	digest := sha256.Sum256(serialized)
	if want := hex.EncodeToString(digest[:]); MessageNode(message, serialized).Revision != want {
		t.Fatalf("revision = %q, want %q", MessageNode(message, serialized).Revision, want)
	}
}

func TestMessageNodeRevisionTracksText(t *testing.T) {
	first := nodeMessage("m1")
	first.Text = protocol.Text("hello")
	second := first
	second.Text = protocol.Text("hello!")
	if MessageNode(first, nil).Revision == MessageNode(second, nil).Revision {
		t.Fatal("different text produced the same revision")
	}
}

func TestMessageNodeTextLengthCountsUTF16Units(t *testing.T) {
	message := nodeMessage("m1")
	message.Text = protocol.Text("a\U0001F600")
	if got := MessageNode(message, nil).TextLength; got != 3 {
		t.Fatalf("textLength = %d, want 3", got)
	}
}

func TestMessageNodeHasContent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*protocol.Message)
		want   bool
	}{
		{"user turn", func(m *protocol.Message) { m.Role = "user" }, true},
		{"empty assistant", func(*protocol.Message) {}, false},
		{"javascript whitespace only", func(m *protocol.Message) {
			m.Text = protocol.Text("\u00a0\u3000\ufeff")
		}, false},
		{"zero width space is content", func(m *protocol.Message) {
			m.Text = protocol.Text("\u200b")
		}, true},
		{"tool activity", func(m *protocol.Message) {
			m.Activity = &protocol.Activity{Type: "tool"}
		}, true},
		{"continuation marker", func(m *protocol.Message) {
			m.TextContinues = protocol.Known(true)
		}, true},
		{"explicit false continuation", func(m *protocol.Message) {
			m.TextContinues = protocol.Known(false)
		}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			message := nodeMessage("m1")
			testCase.mutate(&message)
			if got := MessageNode(message, nil).HasContent; got != testCase.want {
				t.Fatalf("hasContent = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestMessageNodeImageCountDistinguishesAbsentFromEmpty(t *testing.T) {
	absent := nodeMessage("m1")
	if MessageNode(absent, nil).ImageCount.Known {
		t.Fatal("absent imageLinks reported a known count")
	}
	empty := nodeMessage("m1")
	empty.ImageLinks = []string{}
	node := MessageNode(empty, nil)
	if !node.ImageCount.Known || node.ImageCount.Value != 0 {
		t.Fatalf("empty imageLinks = %+v, want a known zero", node.ImageCount)
	}
	pair := nodeMessage("m1")
	pair.ImageLinks = []string{"C:/a.png", "C:/b.png"}
	node = MessageNode(pair, nil)
	if !node.ImageCount.Known || node.ImageCount.Value != 2 {
		t.Fatalf("two imageLinks = %+v, want a known two", node.ImageCount)
	}
}

func TestMessageNodeActivityReportsImageAvailabilityOnly(t *testing.T) {
	plain := nodeMessage("m1")
	plain.Activity = &protocol.Activity{Type: "tool", ToolName: "view_image"}
	node := MessageNode(plain, nil)
	if node.Activity == nil || !node.Activity.HasImage.Known || node.Activity.HasImage.Value {
		t.Fatalf("activity without a reference = %+v, want a known false", node.Activity)
	}
	empty := nodeMessage("m2")
	empty.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known("")}
	node = MessageNode(empty, nil)
	if !node.Activity.HasImage.Known || node.Activity.HasImage.Value {
		t.Fatalf("empty reference = %+v, want a known false", node.Activity.HasImage)
	}
	real := nodeMessage("m3")
	real.Activity = &protocol.Activity{Type: "tool", ImagePath: protocol.Known("C:/secret/image.png")}
	node = MessageNode(real, nil)
	if !node.Activity.HasImage.Value {
		t.Fatalf("present reference = %+v, want true", node.Activity.HasImage)
	}
}

func TestMessageNodeSerializationHidesBodiesAndPaths(t *testing.T) {
	message := nodeMessage("m1")
	message.Text = protocol.Text("secret body")
	message.ImageLinks = []string{"C:/secret/image.png"}
	message.Activity = &protocol.Activity{
		Type:      "tool",
		ToolName:  "view_image",
		Arguments: protocol.Known(protocol.Text("secret arguments")),
		Result:    protocol.Known(protocol.Text("secret result")),
		ImagePath: protocol.Known("C:/secret/image.png"),
	}
	serialized := string(Marshal(MessageNode(message, nil)))
	for _, forbidden := range []string{
		"secret body", "secret arguments", "secret result",
		"imageLinks", "imagePath", "image.png", `"text"`,
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("projection leaked %q: %s", forbidden, serialized)
		}
	}
	if !strings.Contains(serialized, `"hasImage":true`) {
		t.Fatalf("projection lost hasImage: %s", serialized)
	}
	if !strings.Contains(serialized, `"imageCount":1`) {
		t.Fatalf("projection lost imageCount: %s", serialized)
	}
}
