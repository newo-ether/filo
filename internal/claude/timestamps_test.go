package claude

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeTimestampUsesIntegerMillisecondsAndPreservesUnknown(t *testing.T) {
	for _, stamp := range []string{"2026-10-23T00:00:00.123Z", "", "invalid"} {
		event := map[string]json.RawMessage{"timestamp": json.RawMessage(`"` + stamp + `"`)}
		data, err := json.Marshal(nativeTimestamp(event))
		if err != nil {
			t.Fatal(err)
		}
		if stamp == "2026-10-23T00:00:00.123Z" {
			var millis int64
			if err := json.Unmarshal(data, &millis); err != nil || millis != 1792713600123 {
				t.Fatalf("native timestamp must be exact Unix milliseconds: %s, %v", data, err)
			}
		} else if string(data) != "null" {
			t.Fatalf("missing native time was fabricated: %s", data)
		}
	}
}

func TestNativeSessionUpdatedAtUsesCatalogSecondsWithoutFraction(t *testing.T) {
	s, _ := nativeFixture(t)
	s.updated = time.UnixMilli(1792713600123)
	data, err := json.Marshal(s.Session())
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		UpdatedAt int64 `json:"updatedAt"`
	}
	if err := json.Unmarshal(data, &session); err != nil || session.UpdatedAt != 1792713600 {
		t.Fatalf("catalog time must be integer Unix seconds: %s, %v", data, err)
	}
}
