package history

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/newo-ether/filo/internal/nativejson"
)

const nativePrefix = "filo.native."

func encodeCursor(kind string, value any) string {
	body, _ := json.Marshal(value)
	return nativePrefix + kind + "." + base64.RawURLEncoding.EncodeToString(body)
}

func decodeCursor(kind, cursor string) (map[string]any, error) {
	if cursor == "" {
		return nil, nil
	}
	prefix := nativePrefix + kind + "."
	if len(cursor) > 2048 || !strings.HasPrefix(cursor, prefix) {
		return nil, errors.New("Invalid native page cursor")
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, prefix))
	if err != nil {
		return nil, errors.New("Invalid native page cursor")
	}
	value, _, err := nativejson.Read(bytes.NewReader(body), false)
	fields, ok := nativejson.Fields(value)
	if err != nil || !ok {
		return nil, errors.New("Invalid native page cursor")
	}
	return fields, nil
}

func safeInteger(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || math.Abs(number) > 9007199254740991 {
		return 0, false
	}
	return int64(number), true
}

type itemCursor struct {
	ID        string `json:"id"`
	Ordinal   int64  `json:"ordinal"`
	Item      string `json:"item"`
	Inclusive bool   `json:"inclusive,omitempty"`
}

func decodeItems(id, cursor string) (*itemCursor, error) {
	fields, err := decodeCursor("items", cursor)
	if err != nil || fields == nil {
		return nil, err
	}
	thread, idOK := nativejson.AsText(fields["id"])
	item, itemOK := nativejson.AsText(fields["item"])
	ordinal, ordinalOK := safeInteger(fields["ordinal"])
	if !idOK || thread != id || !itemOK || !ordinalOK {
		return nil, errors.New("Invalid native item cursor")
	}
	inclusive, _ := fields["inclusive"].(bool)
	return &itemCursor{thread, ordinal, item, inclusive}, nil
}
