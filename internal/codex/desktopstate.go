package codex

import (
	"errors"
	"math"
	"slices"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// DesktopState flows as a decoded JSON map (the desktop snapshot); every
// mutation path is copy-on-write and never touches prototypes.
type DesktopState = map[string]any

// desktopTurns returns the canonical island order when present, else the
// plain turns array.
func desktopTurns(state map[string]any) []any {
	history, isMap := asMap(state["turnHistory"])
	kind, _ := nativejson.AsText(history["kind"])
	if !isMap || kind != "canonical" {
		turns, _ := state["turns"].([]any)
		return turns
	}
	inner, _ := asMap(history["history"])
	islands, _ := inner["islands"].([]any)
	entities, _ := asMap(inner["entitiesByKey"])
	seen := make(map[string]bool, len(islands)*2)
	turns := make([]any, 0, len(islands)*2)
	for _, islandRaw := range islands {
		island, _ := asMap(islandRaw)
		entries, _ := island["entries"].([]any)
		for _, entryRaw := range entries {
			entry, _ := asMap(entryRaw)
			key, _ := nativejson.AsText(entry["value"])
			if seen[key] {
				continue
			}
			seen[key] = true
			// Set iteration keeps first-occurrence order; entities are
			// filtered with Boolean like the TS chain.
			if entity := entities[key]; truthy(entity) {
				turns = append(turns, entity)
			}
		}
	}
	return turns
}

// DesktopStatus mirrors desktopStatus: an old inProgress turn cannot become
// active once a newer terminal turn exists, and the authoritative native
// runtime status wins when present.
func DesktopStatus(value any) protocol.SessionStatus {
	state, _ := asMap(value)
	turns := desktopTurns(state)
	var latest any
	if n := len(turns); n > 0 {
		latest = turns[n-1]
	}
	runtimeStatus, _ := asMap(state["threadRuntimeStatus"])
	// active = latest?.status === 'inProgress' && (!threadRuntimeStatus ||
	// threadRuntimeStatus.type === 'active') ? latest : undefined
	var active any
	if turnStatus(latest) == "inProgress" {
		if runtimeStatus == nil {
			active = latest
		} else if t, _ := nativejson.AsText(runtimeStatus["type"]); t == "active" {
			active = latest
		}
	}
	var completed any
	for i := len(turns) - 1; i >= 0; i-- {
		if turnStatus(turns[i]) == "completed" {
			completed = turns[i]
			break
		}
	}
	// status = threadRuntimeStatus?.type ?? (active ? 'active' : 'idle');
	// ?? falls through only on null/undefined, so an explicit empty string
	// type is used as-is. A non-string type has no wire representation and
	// falls back here (recorded out-of-range divergence).
	status := ""
	statusKnown := false
	if runtimeStatus != nil {
		if t, present := runtimeStatus["type"]; present && t != nil {
			if s, isStr := nativejson.AsText(t); isStr {
				status, statusKnown = s, true
			}
		}
	}
	if !statusKnown {
		if active != nil {
			status = "active"
		} else {
			status = "idle"
		}
	}
	return protocol.SessionStatus{
		ID:              identityString(state["id"]),
		Status:          &status,
		ActiveTurnID:    turnIDPointer(active),
		CompletedTurnID: turnIDPointer(completed),
		HasUnreadTurn:   state["hasUnreadTurn"] == true,
	}
}

func turnStatus(turn any) string {
	m, _ := asMap(turn)
	s, _ := nativejson.AsText(m["status"])
	return s
}

// turnIDPointer mirrors turn?.turnId ?? null: a present empty string is
// preserved, missing or null yields null.
func turnIDPointer(turn any) *string {
	m, ok := asMap(turn)
	if !ok {
		return nil
	}
	v, present := m["turnId"]
	if !present || v == nil {
		return nil
	}
	if s, isStr := nativejson.AsText(v); isStr {
		return &s
	}
	return nil
}

// ProjectDesktop projects only public native messages; transport snapshots
// never leave Filo unchanged. turnLimit follows the TS falsy gate: 0 means
// "no limit".
func ProjectDesktop(value any, includeActivity bool, turnLimit float64) (protocol.ConversationPage, error) {
	state, ok := asMap(value)
	if !ok {
		return protocol.ConversationPage{}, errors.New("Invalid desktop state")
	}
	allTurns := desktopTurns(state)
	turns := allTurns
	// TS: turnLimit ? allTurns.slice(-turnLimit) : allTurns
	from := math.Trunc(-turnLimit)
	if from < 0 && -from < float64(len(allTurns)) {
		turns = allTurns[len(allTurns)-int(-from):]
	} else if from > 0 {
		if from < float64(len(allTurns)) {
			turns = allTurns[int(from):]
		} else {
			turns = []any{}
		}
	}
	native := make([]NativeTurn, 0, len(turns))
	for _, turnRaw := range turns {
		turn, _ := asMap(turnRaw)
		itemsRaw, isArray := turn["items"].([]any)
		if !isArray {
			return protocol.ConversationPage{}, errors.New("Unsupported desktop item representation")
		}
		items := make([]map[string]any, 0, len(itemsRaw))
		for _, itemRaw := range itemsRaw {
			item, _ := asMap(itemRaw)
			itemType, _ := nativejson.AsText(item["type"])
			if itemType == "steeringUserMessage" {
				if status, _ := nativejson.AsText(item["status"]); status != "accepted" {
					continue
				}
				steer := map[string]any{"type": "userMessage"}
				// id: item.serverUserMessageId ?? item.id
				if serverID, has := item["serverUserMessageId"]; has && serverID != nil {
					steer["id"] = serverID
				} else {
					steer["id"] = item["id"]
				}
				steer["clientId"] = item["clientUserMessageId"]
				steer["content"] = item["input"]
				items = append(items, steer)
				continue
			}
			// Native stores an already rendered steer as a marker beside the
			// accepted input.
			if itemType == "steered" {
				continue
			}
			items = append(items, item)
		}
		// startedAt: (turn.turnStartedAtMs ?? 0) / 1000
		startedMs := 0.0
		if ms, isNum := turn["turnStartedAtMs"].(float64); isNum {
			startedMs = ms
		}
		startedAt := startedMs / 1000
		native = append(native, NativeTurn{
			ID:        identityString(turn["turnId"]),
			Items:     items,
			StartedAt: &startedAt,
			Status:    turn["status"],
			Error:     turn["error"],
		})
	}
	// TS double reverse: projectDesktop calls native.reverse() and
	// projectMessages reverses again, netting oldest-first output. Our
	// ProjectMessages already reverses internally, so reverse once here.
	slices.Reverse(native)
	messages := ProjectMessages(native, includeActivity)
	// new Map(messages.map(...)): later duplicates overwrite the value but
	// keep the first insertion position.
	byID := make(map[string]int, len(messages))
	ordered := make([]protocol.Message, 0, len(messages))
	for _, message := range messages {
		if idx, exists := byID[message.ID]; exists {
			ordered[idx] = message
			continue
		}
		byID[message.ID] = len(ordered)
		ordered = append(ordered, message)
	}
	status := DesktopStatus(state)
	runtime := &protocol.Runtime{
		// DesktopStatus always resolves status to a concrete string.
		Status:          *status.Status,
		ActiveTurnID:    status.ActiveTurnID,
		Model:           stringPointer(state["latestModel"]), // ?? null
		Effort:          runtimeEffort(state),
		ServiceTier:     serviceTier(state),
		CompletedTurnID: protocol.Known(status.CompletedTurnID),
	}
	if usage, isMap := asMap(state["latestTokenUsageInfo"]); isMap {
		last, _ := asMap(usage["last"])
		if tokens, isNum := last["totalTokens"].(float64); isNum {
			value := tokens
			runtime.ContextTokens = &value
		}
		if window, isNum := usage["modelContextWindow"].(float64); isNum {
			value := window
			runtime.ContextWindow = &value
		}
	}
	return protocol.ConversationPage{
		Messages:   ordered,
		NextCursor: nil,
		Queued:     []protocol.QueuedInput{},
		Runtime:    runtime,
	}, nil
}

// runtimeEffort mirrors latestThreadSettings?.effort ?? latestReasoningEffort:
// null/undefined effort falls through, undefined ends the chain as absent.
func runtimeEffort(state map[string]any) protocol.Optional[*string] {
	if settings, isMap := asMap(state["latestThreadSettings"]); isMap {
		if v, has := settings["effort"]; has && v != nil {
			return protocol.Known(stringPointer(v))
		}
	}
	v, has := state["latestReasoningEffort"]
	if !has {
		return protocol.Optional[*string]{}
	}
	if v == nil {
		return protocol.Known[*string](nil)
	}
	return protocol.Known(stringPointer(v))
}

// serviceTier mirrors latestThreadSettings?.serviceTier with no fallback.
func serviceTier(state map[string]any) protocol.Optional[*string] {
	settings, isMap := asMap(state["latestThreadSettings"])
	if !isMap {
		return protocol.Optional[*string]{}
	}
	v, has := settings["serviceTier"]
	if !has {
		return protocol.Optional[*string]{}
	}
	if v == nil {
		return protocol.Known[*string](nil)
	}
	return protocol.Known(stringPointer(v))
}
