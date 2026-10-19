package codex

import (
	"errors"

	"github.com/newo-ether/filo/internal/nativejson"
)

// hostReader exposes the request operation needed to verify a selected auxiliary
// host. Verification never acquires, stops or migrates a native task.
type hostReader interface {
	Request(method string, params any) (any, error)
}

// VerifyHostIdle walks the loaded-thread pages of the auxiliary host and
// requires every visible thread to report an idle status. It returns the
// number of verified threads. Pages must contain arrays of distinct string IDs,
// stay within 10000 IDs and provide advancing string cursors.
func VerifyHostIdle(rpc hostReader) (int, error) {
	seen := make(map[string]struct{})
	cursors := make(map[string]struct{})
	// A nil cursor omits the field; an empty next cursor ends pagination.
	var cursor *string
	for {
		params := map[string]any{"limit": 100}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		pageResult, err := rpc.Request("thread/loaded/list", params)
		if err != nil {
			return 0, err
		}
		// Missing, null and non-array data cannot prove that the host is idle.
		page, _ := nativejson.Fields(pageResult)
		dataRaw, _ := page["data"].([]any)
		if dataRaw == nil {
			return 0, errors.New("Cannot verify auxiliary native task state")
		}
		for _, entry := range dataRaw {
			id, isString := nativejson.AsText(entry)
			_, duplicate := seen[id]
			if !isString || duplicate || len(seen) >= 10000 {
				return 0, errors.New("Invalid auxiliary task catalog")
			}
			seen[id] = struct{}{}
			readResult, err := rpc.Request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
			if err != nil {
				return 0, err
			}
			// Missing or malformed status cannot prove that the thread is idle.
			readMap, _ := nativejson.Fields(readResult)
			thread, _ := nativejson.Fields(readMap["thread"])
			status, _ := nativejson.Fields(thread["status"])
			if statusType, _ := nativejson.AsText(status["type"]); statusType != "idle" {
				return 0, &RpcError{
					Message: "A Filo-owned auxiliary task is active; finish it before updating",
					Code:    f64(-32600),
				}
			}
		}
		// Present cursors must be strings and must not repeat.
		if nextRaw := page["nextCursor"]; nextRaw != nil {
			next, isString := nativejson.AsText(nextRaw)
			_, seenCursor := cursors[next]
			if !isString || seenCursor {
				return 0, errors.New("Auxiliary cursor did not advance")
			}
			// Track nonempty cursors; an empty cursor ends the scan.
			if next != "" {
				cursors[next] = struct{}{}
				nextCopy := next
				cursor = &nextCopy
			} else {
				cursor = nil
			}
		} else {
			cursor = nil
		}
		if cursor == nil {
			return len(seen), nil
		}
	}
}

func f64(v float64) *float64 { return &v }
