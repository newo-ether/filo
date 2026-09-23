package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// taskEventResponse builds one response over a synthetic event stream body.
func taskEventResponse(status int, contentType, body string) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestReadTaskEventsRefusesAnUnavailableStream pins the stream gate: only an
// accepted event stream is read at all.
func TestReadTaskEventsRefusesAnUnavailableStream(t *testing.T) {
	refused := []*http.Response{
		nil,
		taskEventResponse(500, "text/event-stream", ""),
		taskEventResponse(200, "application/json", "{}"),
		{StatusCode: 200, Header: http.Header{}},
	}
	for _, response := range refused {
		err := ReadTaskEvents(response, func(any) error { return nil })
		if err == nil || err.Error() != "Filo task stream is unavailable" {
			t.Fatalf("an unavailable stream = %v", err)
		}
	}
}

// TestReadTaskEventsParsesBoundedRecords pins record framing: data lines
// accumulate, an event name applies to its own record, and the stream always
// ends in a failure rather than a clean return.
func TestReadTaskEventsParsesBoundedRecords(t *testing.T) {
	body := "data: {\"page\":1}\r\n" +
		"\n" +
		"event: ping\ndata: {\"page\":2}\n\n" +
		"data: [1,\ndata: 2,\ndata: 3]\n\n"
	var received []any
	err := ReadTaskEvents(taskEventResponse(200, "text/event-stream; charset=utf-8", body),
		func(value any) error {
			received = append(received, value)
			return nil
		})
	if err == nil || err.Error() != "Filo task stream disconnected" {
		t.Fatalf("a bounded stream = %v", err)
	}
	if len(received) != 3 {
		t.Fatalf("the received records = %v", received)
	}
	first, firstOK := received[0].(map[string]any)
	second, secondOK := received[1].(map[string]any)
	if !firstOK || first["page"] != float64(1) || !secondOK || second["page"] != float64(2) {
		t.Fatalf("the decoded records = %v", received)
	}
	// Three data lines join with one newline each, so a payload split across
	// lines still parses as the record it is.
	joined, joinedOK := received[2].([]any)
	if !joinedOK || len(joined) != 3 || joined[2] != float64(3) {
		t.Fatalf("the joined record = %v", received)
	}
}

// TestReadTaskEventsFailsOnTheErrorMarker pins the error record: an `error`
// event fails the stream before any payload of it is delivered.
func TestReadTaskEventsFailsOnTheErrorMarker(t *testing.T) {
	delivered := 0
	err := ReadTaskEvents(taskEventResponse(200, "text/event-stream", "event: error\ndata: {}\n\n"),
		func(any) error {
			delivered++
			return nil
		})
	if err == nil || err.Error() != "Filo task stream failed" {
		t.Fatalf("an error record = %v", err)
	}
	if delivered != 0 {
		t.Fatalf("an error record still delivered %d payloads", delivered)
	}
}

// TestReadTaskEventsBoundsOneRecord pins the byte ceiling of one pending record.
func TestReadTaskEventsBoundsOneRecord(t *testing.T) {
	body := "data: " + strings.Repeat("x", taskEventLimit) + "\n\n"
	err := ReadTaskEvents(taskEventResponse(200, "text/event-stream", body), func(any) error { return nil })
	if err == nil || err.Error() != "Task event exceeds the response limit" {
		t.Fatalf("an oversized record = %v", err)
	}
}

// TestReadTaskEventsRejectsInvalidPayloads pins the two payload failures: an
// invalid UTF-8 sequence and a malformed JSON record both fail the stream.
func TestReadTaskEventsRejectsInvalidPayloads(t *testing.T) {
	response := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: \xc3(\n\n")),
	}
	err := ReadTaskEvents(response, func(any) error { return nil })
	if err == nil || err.Error() != "The encoded data was not valid for encoding utf-8" {
		t.Fatalf("an invalid sequence = %v", err)
	}
	delivered := 0
	err = ReadTaskEvents(taskEventResponse(200, "text/event-stream", "data: {not json}\n\n"),
		func(any) error {
			delivered++
			return nil
		})
	if err == nil || delivered != 0 {
		t.Fatalf("a malformed record = %v, %d payloads", err, delivered)
	}
}

// TestReadTaskEventsPropagatesAReceiveFailure pins the reader's own failure: a
// receiver that rejects one page ends the stream with that failure.
func TestReadTaskEventsPropagatesAReceiveFailure(t *testing.T) {
	err := ReadTaskEvents(taskEventResponse(200, "text/event-stream", "data: {\"page\":1}\n\n"),
		func(any) error { return errors.New("the reader refused the page") })
	if err == nil || err.Error() != "the reader refused the page" {
		t.Fatalf("a reader failure = %v", err)
	}
}

// TestReadTaskEventsJoinsAndTrimsNothingElse pins the framing details: exactly
// one space after `data:` is framing, and only the event name is trimmed.
func TestReadTaskEventsJoinsAndTrimsNothingElse(t *testing.T) {
	var received any
	err := ReadTaskEvents(taskEventResponse(200, "text/event-stream",
		"data:  {\"space\":1}\n\nevent:   \n\n"),
		func(value any) error {
			received = value
			return nil
		})
	if err == nil || err.Error() != "Filo task stream disconnected" {
		t.Fatalf("a framed stream = %v", err)
	}
	object, ok := received.(map[string]any)
	if !ok || object["space"] != float64(1) {
		t.Fatalf("the decoded record = %v", received)
	}
}
