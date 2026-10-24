package codex

import (
	"errors"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

// NativeLiveState projects one already-loaded native thread. It never resumes,
// launches or scans history. Notifications bridge concurrent native reads.
type NativeLiveState struct {
	Rpc     *Rpc
	ID      string
	mu      sync.Mutex
	turn    map[string]any
	hasUser bool
	version uint64
}

func (s *NativeLiveState) Receive(method string, params any) {
	fields, _ := nativejson.Fields(params)
	if nativejson.Text(fields["threadId"]) != s.ID {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "turn/started", "turn/completed":
		turn, ok := nativejson.Fields(fields["turn"])
		if ok && nativejson.Text(turn["id"]) != "" {
			same := nativejson.Text(turn["id"]) == nativejson.Text(s.turn["id"])
			s.hasUser = nativeTurnHasUser(turn) || same && s.hasUser
			s.turn = turn
			s.version++
		}
	case "item/started", "item/completed":
		item, _ := nativejson.Fields(fields["item"])
		if nativejson.Text(item["type"]) == "userMessage" &&
			nativejson.Text(fields["turnId"]) == nativejson.Text(s.turn["id"]) {
			s.hasUser = true
			s.version++
		}
	}
}

// State uses the latest bounded native turn page, including in-progress turns.
// A concurrent notification wins over the read that was already in flight.
func (s *NativeLiveState) State(id string) (any, error) {
	if id != s.ID {
		return nil, errors.New("Native task identity mismatch")
	}
	s.mu.Lock()
	version := s.version
	s.mu.Unlock()
	value, err := s.Rpc.RequestTimeout("thread/read", map[string]any{"threadId": id, "includeTurns": false}, 5*time.Second)
	if err != nil {
		return nil, err
	}
	fields, _ := nativejson.Fields(value)
	thread, _ := nativejson.Fields(fields["thread"])
	status, _ := nativejson.Fields(thread["status"])
	kind := nativejson.Text(status["type"])
	if nativejson.Text(thread["id"]) != id || kind == "" || kind == "notLoaded" {
		return nil, errors.New("Native task is no longer loaded")
	}
	value, err = s.Rpc.RequestTimeout("thread/turns/list", map[string]any{
		"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "summary",
	}, 5*time.Second)
	if err != nil {
		return nil, err
	}
	fields, _ = nativejson.Fields(value)
	turns, ok := fields["data"].([]any)
	if !ok || len(turns) > 1 {
		return nil, errors.New("Invalid native turn page")
	}
	var latest map[string]any
	if len(turns) > 0 {
		latest, _ = nativejson.Fields(turns[0])
		if nativejson.Text(latest["id"]) == "" || nativejson.Text(latest["status"]) == "" {
			return nil, errors.New("Invalid native turn identity")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version == version {
		same := nativejson.Text(latest["id"]) == nativejson.Text(s.turn["id"])
		s.hasUser = nativeTurnHasUser(latest) || same && s.hasUser
		s.turn = latest
	}
	// Never turn an active native status with no exact running turn into idle.
	if nativejson.Text(s.turn["status"]) == "inProgress" {
		thread["status"] = map[string]any{"type": "active"}
	}
	return map[string]any{"thread": thread, "materialized": true, "hasUser": s.hasUser, "turn": s.turn}, nil
}

func nativeTurnHasUser(turn map[string]any) bool {
	items, _ := turn["items"].([]any)
	for _, value := range items {
		item, _ := nativejson.Fields(value)
		if nativejson.Text(item["type"]) == "userMessage" {
			return true
		}
	}
	return false
}
