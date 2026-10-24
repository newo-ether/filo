package claude

import (
	"encoding/json"

	"github.com/newo-ether/filo/internal/protocol"
)

func nativeRowBytes(row protocol.Message) int {
	data, err := json.Marshal(row)
	if err != nil {
		return maxRowBytes + 1
	}
	return len(data)
}

// putNativeRow updates one stable identity in place. Overflow removes the whole
// row; a discarded prefix must never reappear as an apparently complete suffix.
func (s *NativeSession) putNativeRow(row protocol.Message) bool {
	position := -1
	for index := range s.rows {
		if s.rows[index].ID == row.ID {
			position = index
			break
		}
	}
	if position >= 0 {
		s.rowBytes -= nativeRowBytes(s.rows[position])
	}
	size := nativeRowBytes(row)
	if size > maxRowBytes {
		if position >= 0 {
			s.rows = append(s.rows[:position], s.rows[position+1:]...)
		}
		return false
	}
	if position < 0 {
		s.rows = append(s.rows, row)
	} else {
		s.rows[position] = row
	}
	s.rowBytes += size
	for len(s.rows) > maxRows || s.rowBytes > maxSessionBytes {
		s.rowBytes -= nativeRowBytes(s.rows[0])
		s.rows = s.rows[1:]
	}
	return true
}

func (s *NativeSession) nativeRow(id string) (protocol.Message, bool) {
	for _, row := range s.rows {
		if row.ID == id {
			if row.Activity != nil {
				activity := *row.Activity
				row.Activity = &activity
			}
			return row, true
		}
	}
	return protocol.Message{}, false
}

func nativeRowsSnapshot(rows []protocol.Message) []protocol.Message {
	result := append([]protocol.Message{}, rows...)
	for index := range result {
		row := &result[index]
		if row.Activity != nil {
			activity := *row.Activity
			row.Activity = &activity
		}
		if row.ClientID != nil {
			client := *row.ClientID
			row.ClientID = &client
		}
	}
	return result
}
