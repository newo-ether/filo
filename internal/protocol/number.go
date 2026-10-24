package protocol

import (
	"encoding/json"
	"math"
)

// Number preserves native JSON's null representation for unavailable numeric
// results. An invalid native timestamp must not prevent other message fields
// or the entire history page from being serialized.
type Number float64

func (number *Number) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*number = Number(math.NaN())
		return nil
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*number = Number(value)
	return nil
}

func (number Number) MarshalJSON() ([]byte, error) {
	value := float64(number)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return []byte("null"), nil
	}
	return json.Marshal(value)
}
