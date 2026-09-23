package sessions

import (
	"context"
	"math"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/nativejson"
)

func fields(value any) map[string]any { result, _ := nativejson.Fields(value); return result }
func text(value any) string           { return nativejson.Text(value) }

func (f *Followers) owner(ctx context.Context, id string) (string, error) {
	f.mu.Lock()
	stopped := f.stopped
	f.mu.Unlock()
	if stopped {
		return "", unavailable()
	}
	response, err := f.ipc.Request(ctx, "thread-owner-discovery", 1, map[string]any{"hostId": "local", "conversationId": id}, "")
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return "", unavailable()
	}
	if text(response["resultType"]) == "error" && text(response["error"]) == "no-client-found" {
		return "", nil
	}
	owner := text(response["handledByClientId"])
	if text(response["resultType"]) != "success" || owner == "" || fields(response["result"])["supportsUntrustedAppInput"] != true {
		return "", unavailable()
	}
	f.seen[id] = true
	return owner, nil
}

func (f *Followers) Receive(message desktop.Message) {
	p := fields(message["params"])
	if text(p["hostId"]) != "local" {
		return
	}
	id := text(p["conversationId"])
	f.mu.Lock()
	entry := f.entries[id]
	if entry == nil || f.stopped {
		f.mu.Unlock()
		return
	}
	if text(message["method"]) == "thread-stream-following-status-requested" && message["version"] == float64(1) {
		f.mu.Unlock()
		if err := f.follow(entry, true); err != nil {
			f.mu.Lock()
			dropped := f.dropLocked(entry, err)
			f.mu.Unlock()
			if dropped {
				_ = f.follow(entry, false)
				f.closed()
			}
		}
		return
	}
	if text(message["method"]) != "thread-stream-state-changed" || message["version"] != float64(11) || text(message["sourceClientId"]) != entry.owner {
		f.mu.Unlock()
		return
	}
	change := fields(p["change"])
	revision, valid := change["revision"].(float64)
	valid = valid && !math.IsNaN(revision) && !math.IsInf(revision, 0) && math.Trunc(revision) == revision && revision >= 0 && revision <= 9007199254740991
	if valid && int64(revision) <= entry.revision {
		f.mu.Unlock()
		return
	}
	if valid {
		switch text(change["type"]) {
		case "snapshot":
			state := fields(change["conversationState"])
			valid = text(state["id"]) == id && text(state["hostId"]) == "local"
			if valid {
				entry.state = state
			}
		case "patches":
			base, ok := change["baseRevision"].(float64)
			valid = ok && base == float64(entry.revision) && entry.state != nil
			if valid {
				state, err := codex.ApplyDesktopPatches(entry.state, change["patches"])
				valid = err == nil && text(fields(state)["id"]) == id && text(fields(state)["hostId"]) == "local"
				if valid {
					entry.state = fields(state)
				}
			}
		default:
			valid = false
		}
	}
	if !valid {
		f.dropLocked(entry, unavailable())
		f.mu.Unlock()
		_ = f.follow(entry, false)
		f.closed()
		return
	}
	entry.revision = int64(revision)
	delete(f.retry, id)
	f.settleLocked(entry, nil)
	f.mu.Unlock()
	f.changed(id)
}
