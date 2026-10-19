package history

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"math"
	"os"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// ReadRuntime projects bounded persisted evidence. Only a directly observed
// latest terminal establishes idle; missing state remains notLoaded.
func ReadRuntime(ctx context.Context, path, id string, model, effort *string, receiptOutput ...*[]protocol.InputReceipt) (*protocol.Runtime, error) {
	runtime := &protocol.Runtime{Status: "notLoaded", Model: model, Effort: protocol.Known(effort)}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	header := make([]byte, min(int64(scanBytes), info.Size()))
	n, err := file.ReadAt(header, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	end := bytes.IndexByte(header[:n], '\n')
	if end < 0 {
		return nil, errors.New("Native history identity mismatch")
	}
	value, _, err := nativejson.Read(bytes.NewReader(header[:end]), false)
	fields, _ := nativejson.Fields(value)
	identity, _ := nativejson.Fields(fields["payload"])
	if err != nil || nativejson.Text(fields["type"]) != "session_meta" || nativejson.Text(identity["id"]) != id {
		return nil, errors.New("Native history identity mismatch")
	}
	settings, usage, terminal := false, false, false
	var receipts []protocol.InputReceipt
	scanned := 0
	for span, err := range records(ctx, file, info.Size(), max(int64(0), info.Size()-8*1024*1024)) {
		if err != nil {
			return nil, err
		}
		if scanned >= 4096 {
			break
		}
		scanned++
		if span.end-span.start > scanBytes {
			continue
		}
		value, _, err := nativejson.Read(contextReader{ctx, io.NewSectionReader(file, span.start, span.end-span.start)}, false)
		if err != nil {
			return nil, err
		}
		fields, _ := nativejson.Fields(value)
		payload, _ := nativejson.Fields(fields["payload"])
		if nativejson.Text(fields["type"]) != "event_msg" {
			continue
		}
		switch nativejson.Text(payload["type"]) {
		case "item_completed":
			if nativejson.Text(payload["thread_id"]) != id {
				return nil, errors.New("Native item identity mismatch")
			}
			item, _ := nativejson.Fields(payload["item"])
			if nativejson.Text(item["type"]) == "UserMessage" {
				receipts = append(receipts, protocol.InputReceipt{ClientID: nativejson.Text(item["client_id"]),
					TurnID: nativejson.Text(payload["turn_id"]), MessageID: nativejson.Text(item["id"])})
			}
		case "task_complete", "task_started", "turn_aborted":
			if terminal {
				continue
			}
			terminal = true
			turn := nativejson.Text(payload["turn_id"])
			if nativejson.Text(payload["type"]) != "task_started" && turn != "" {
				runtime.Status = "idle"
				runtime.CompletedTurnID = protocol.Known(&turn)
			} else {
				// A later start must suppress older indexed completion evidence.
				runtime.CompletedTurnID = protocol.Known[*string](nil)
			}
		case "thread_settings_applied":
			if settings {
				continue
			}
			if nativejson.Text(payload["thread_id"]) != id {
				return nil, errors.New("Native settings identity mismatch")
			}
			settings = true
			values, _ := nativejson.Fields(payload["thread_settings"])
			if value, ok := nativejson.AsText(values["model"]); ok {
				runtime.Model = &value
			}
			if value, ok := nativejson.AsText(values["reasoning_effort"]); ok {
				runtime.Effort = protocol.Known(&value)
			}
			if value, present := values["service_tier"]; present {
				if value == nil {
					runtime.ServiceTier = protocol.Known[*string](nil)
				} else if text, ok := nativejson.AsText(value); ok {
					runtime.ServiceTier = protocol.Known(&text)
				}
				runtime.ServiceTierKnown = protocol.Known(runtime.ServiceTier.Known)
			}
		case "token_count":
			if usage {
				continue
			}
			values, _ := nativejson.Fields(payload["info"])
			if values == nil {
				continue
			}
			usage = true
			last, _ := nativejson.Fields(values["last_token_usage"])
			runtime.ContextTokens = runtimeNumber(last["total_tokens"])
			runtime.ContextWindow = runtimeNumber(values["model_context_window"])
		}
	}
	// Records were visited newest first; the shared bound retains newest IDs.
	for left, right := 0, len(receipts)-1; left < right; left, right = left+1, right-1 {
		receipts[left], receipts[right] = receipts[right], receipts[left]
	}
	if len(receiptOutput) > 0 && receiptOutput[0] != nil {
		*receiptOutput[0] = protocol.MergeInputReceipts(receipts)
	}
	return runtime, ctx.Err()
}

func runtimeNumber(value any) *float64 {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return nil
	}
	return &number
}

// An asynchronous index cannot prove the absence of a newer rollout start.
// Only ReadRuntime's directly observed terminal may establish completion. Keep
// missing or bounded-out evidence unknown, including when the index is stale.
func indexedCompletion(_ context.Context, _ *sql.Tx, _ string, _ *protocol.Runtime) error {
	return nil
}
