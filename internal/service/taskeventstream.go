package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	// taskEventStreamType is the only content type a task event stream may carry.
	taskEventStreamType = "text/event-stream"
	// taskEventLimit bounds one pending line plus one accumulated data payload.
	// Mirrors the TS `1024 * 1024` byte ceiling.
	taskEventLimit = 1024 * 1024
	// taskEventMarker is the SSE event name that reports a failed task stream.
	taskEventMarker = "error"
)

// ReadTaskEvents reads bounded SSE records from one task event response and
// hands every parsed page to receive. It never retains an unbounded line or a
// stream history, and it fails instead of returning on a disconnected stream, so
// a caller can never mistake a truncated subscription for an ended one.
//
// Divergence from the TS `readTaskEvents(response, receive)`: receive returns an
// error, which is the Go form of the TS guard throwing inside the receive
// callback. The decoder is fatal like the TS `TextDecoder('utf-8', {fatal:true})`,
// so an invalid sequence fails the stream instead of being replaced.
func ReadTaskEvents(response *http.Response, receive func(value any) error) error {
	if response == nil || response.Body == nil || response.StatusCode < 200 ||
		response.StatusCode >= 300 ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), taskEventStreamType) {
		cancelTaskEventResponse(response)
		return errors.New("Filo task stream is unavailable")
	}
	defer response.Body.Close()
	decoder := &taskEventDecoder{}
	pending, data, event := "", "", ""
	buffer := make([]byte, 8192)
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			text, err := decoder.decode(buffer[:read])
			if err != nil {
				return err
			}
			pending += text
			if len(pending)+len(data) > taskEventLimit {
				return errors.New("Task event exceeds the response limit")
			}
			for {
				end := strings.IndexByte(pending, '\n')
				if end < 0 {
					break
				}
				line := strings.TrimSuffix(pending[:end], "\r")
				pending = pending[end+1:]
				switch {
				case line == "":
					if event == taskEventMarker {
						return errors.New("Filo task stream failed")
					}
					if data != "" {
						var value any
						if err := json.Unmarshal([]byte(data), &value); err != nil {
							return err
						}
						if err := receive(value); err != nil {
							return err
						}
					}
					data, event = "", ""
				case strings.HasPrefix(line, "data:"):
					// Only a single space after the field separator is framing,
					// exactly like the TS `replace(/^ /, '')`.
					value := strings.TrimPrefix(line[len("data:"):], " ")
					if data != "" {
						data += "\n"
					}
					data += value
				case strings.HasPrefix(line, "event:"):
					event = strings.TrimSpace(line[len("event:"):])
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}
	return errors.New("Filo task stream disconnected")
}

// cancelTaskEventResponse releases the body of a refused stream best effort.
func cancelTaskEventResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}

// taskEventDecoder mirrors a fatal streaming UTF-8 decoder: an incomplete
// trailing rune is held until the next chunk, and any other invalid sequence is
// an error rather than a replacement character.
type taskEventDecoder struct {
	held []byte
}

// decode returns the decodable prefix of one chunk.
func (d *taskEventDecoder) decode(chunk []byte) (string, error) {
	combined := make([]byte, 0, len(d.held)+len(chunk))
	combined = append(combined, d.held...)
	combined = append(combined, chunk...)
	d.held = nil
	// Hold back a rune that a later chunk may still complete.
	for back := 1; back <= 3 && back <= len(combined); back++ {
		start := len(combined) - back
		if combined[start]&0xC0 == 0x80 {
			continue
		}
		if size := utf8SequenceLength(combined[start]); size > back {
			d.held = append([]byte(nil), combined[start:]...)
			combined = combined[:start]
		}
		break
	}
	if !utf8.Valid(combined) {
		return "", errors.New("The encoded data was not valid for encoding utf-8")
	}
	return string(combined), nil
}

// utf8SequenceLength is the encoded length a leading byte announces.
func utf8SequenceLength(lead byte) int {
	switch {
	case lead >= 0xF0:
		return 4
	case lead >= 0xE0:
		return 3
	case lead >= 0xC0:
		return 2
	}
	return 1
}
