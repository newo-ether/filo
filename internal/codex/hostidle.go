package codex

import (
	"errors"

	"github.com/newo-ether/filo/internal/nativejson"
)

// hostReader mirrors TS Pick<CodexRpc, 'request'>. VerifyHostIdle reads the
// selected auxiliary host only; it never acquires, stops or migrates a native
// task.
type hostReader interface {
	Request(method string, params any) (any, error)
}

// VerifyHostIdle walks the loaded-thread pages of the auxiliary host and
// requires every visible thread to report an idle status. It returns the
// number of verified threads. Error texts and guard rails (array shape,
// distinct string ids, 10000-id ceiling, advancing string cursors) match the
// TS verifyHostIdle verbatim.
func VerifyHostIdle(rpc hostReader) (int, error) {
	seen := make(map[string]struct{})
	cursors := make(map[string]struct{})
	// nil models the TS undefined cursor; empty strings are normalised to nil
	// by the same "page.nextCursor || undefined" expression as in TS.
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
		// TS reads page.data off any value; a missing page, missing "data" or
		// JSON null all fail Array.isArray and yield the same "cannot verify"
		// error. A decoded JSON array is never nil, so dataRaw == nil covers
		// exactly the TS rejection set.
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
			// TS thread?.status?.type !== 'idle': a missing thread, status or
			// wrong type all take the same "active" branch.
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
		// TS: page.nextCursor != null && (typeof != 'string' || cursors.has) -> throw
		if nextRaw := page["nextCursor"]; nextRaw != nil {
			next, isString := nativejson.AsText(nextRaw)
			_, seenCursor := cursors[next]
			if !isString || seenCursor {
				return 0, errors.New("Auxiliary cursor did not advance")
			}
			// TS: cursor = page.nextCursor || undefined; if (cursor) cursors.add(cursor)
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
