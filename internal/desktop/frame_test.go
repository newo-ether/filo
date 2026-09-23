package desktop

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

type chunkedReader struct {
	data []byte
	pos  int
	size int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.pos == len(r.data) {
		return 0, io.EOF
	}
	n := r.size
	if n > len(p) {
		n = len(p)
	}
	if remaining := len(r.data) - r.pos; n > remaining {
		n = remaining
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func frame(t *testing.T, body string) []byte {
	t.Helper()
	if !json.Valid([]byte(body)) {
		t.Fatalf("test body is not JSON: %s", body)
	}
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out
}

func TestCoalescedAndByteFragmentedFramesPreserveValues(t *testing.T) {
	stream := append(frame(t, `{"text":"你好 🌍"}`), frame(t, `{"text":"Next"}`)...)
	values := []string{"你好 🌍", "Next"}
	reader := &chunkedReader{data: stream, size: 1}
	for index, want := range values {
		value, err := ReadFrame(reader)
		if err != nil {
			t.Fatalf("frame %d: %v", index, err)
		}
		got, ok := nativejson.Fields(value)
		if !ok || got["text"] != want {
			t.Fatalf("frame %d = %v want %q", index, value, want)
		}
	}
	if _, err := ReadFrame(reader); err != io.EOF {
		t.Fatalf("after stream = %v", err)
	}
}

func TestLongFrameSurvivesPipeFragmentationAndAdjacentFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("memory-heavy baseline case")
	}
	size := 20 * 1024 * 1024
	big := frame(t, `{"text":"`+strings.Repeat("x", size)+`"}`)
	tail := frame(t, `{"text":"tail"}`)
	reader := &chunkedReader{data: append(big, tail...), size: 65536 + 7}
	first, err := ReadFrame(reader)
	if err != nil {
		t.Fatalf("long frame: %v", err)
	}
	if text, ok := frameObject(first)["text"].(string); !ok || len(text) != size {
		t.Fatal("long frame text not retained intact")
	}
	second, err := ReadFrame(reader)
	if err != nil {
		t.Fatalf("adjacent frame: %v", err)
	}
	if frameObject(second)["text"] != "tail" {
		t.Fatal("adjacent frame lost")
	}
}

func TestInvalidDeclaredSizeFailsBeforePayload(t *testing.T) {
	for _, size := range []uint32{0, MaxFrameBytes + 1} {
		header := make([]byte, 4)
		binary.LittleEndian.PutUint32(header, size)
		reader := &chunkedReader{data: append(header, []byte("payload")...), size: 1 << 20}
		if _, err := ReadFrame(reader); err == nil || !strings.Contains(err.Error(), "invalid IPC frame size") {
			t.Fatalf("size %d error = %v", size, err)
		}
		if reader.pos != 4 {
			t.Fatalf("size %d consumed %d bytes", size, reader.pos)
		}
	}
}

func TestTruncatedBodyAndTrailingGarbageFailClosed(t *testing.T) {
	truncated := frame(t, `{"text":"complete"}`)[:10]
	if _, err := ReadFrame(bytes.NewReader(truncated)); err == nil {
		t.Fatal("truncated frame accepted")
	}
	body := []byte(`{"a":1}x`)
	garbage := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(garbage, uint32(len(body)))
	copy(garbage[4:], body)
	if _, err := ReadFrame(bytes.NewReader(garbage)); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("garbage error = %v", err)
	}
	spaced := []byte(`{"a":1}   `)
	withSpaces := make([]byte, 4+len(spaced))
	binary.LittleEndian.PutUint32(withSpaces, uint32(len(spaced)))
	copy(withSpaces[4:], spaced)
	if _, err := ReadFrame(bytes.NewReader(withSpaces)); err != nil {
		t.Fatalf("in-frame trailing whitespace rejected: %v", err)
	}
}

func TestWriteFrameRoundTripAndMarshalFailure(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteFrame(&wire, map[string]any{"text": "你好 🌍"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	value, err := ReadFrame(&wire)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if frameObject(value)["text"] != "你好 🌍" {
		t.Fatalf("round trip = %v", value)
	}
	if err := WriteFrame(io.Discard, make(chan int)); err == nil {
		t.Fatal("unmarshalable value accepted")
	}
	var header [4]byte
	empty := &bytes.Buffer{}
	if err := WriteFrame(empty, nil); err != nil {
		t.Fatalf("nil write: %v", err)
	}
	raw, _ := io.ReadAll(empty)
	binary.LittleEndian.PutUint32(header[:], uint32(len(raw)-4))
	if !bytes.Equal(raw[:4], header[:4]) {
		t.Fatalf("header mismatch: %v vs body %d", raw[:4], len(raw)-4)
	}
}

func frameObject(value any) map[string]any { fields, _ := nativejson.Fields(value); return fields }
