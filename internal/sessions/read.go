package sessions

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

type persistedRead struct {
	key    string
	done   chan struct{}
	cancel context.CancelFunc
	page   protocol.ConversationPage
	err    error
}

// Read projects one conversation. The caller's exclusion list is deliberately
// not consulted here: the live projection this surface already followed is the
// only authority on which turns it merged, exactly as the TS three-argument
// read behaves. The parameter exists so the desktop surface satisfies the
// shared session API, where a worker layer passes its own exclusions through.
func (sessions *DesktopSessions) Read(ctx context.Context, id, cursor string, activity bool,
	excludedTurns []string) (protocol.ConversationPage, error) {
	var empty protocol.ConversationPage
	owner, err := sessions.f.owner(ctx, id)
	if err != nil {
		return empty, err
	}
	if owner == "" {
		created, err := sessions.created(ctx, id)
		if err != nil {
			return empty, err
		}
		if created {
			return sessions.options.Created.Read(ctx, id, cursor, activity)
		}
		page, err := sessions.options.History.Read(ctx, id, cursor, activity, nil)
		sessions.f.mu.Lock()
		seen := sessions.f.seen[id]
		sessions.f.mu.Unlock()
		if seen {
			page = notLoaded(page)
		}
		return page, err
	}
	entry, release, err := sessions.readState(ctx, id, owner, false)
	if err != nil {
		return empty, err
	}
	defer release()
	if entry == nil {
		page, err := sessions.options.History.Read(ctx, id, cursor, activity, nil)
		return notLoaded(page), err
	}
	state, err := sessions.f.snapshot(entry)
	if err != nil {
		return empty, err
	}
	live, err := codex.ProjectDesktop(state, activity, 128)
	if err != nil {
		return empty, err
	}
	var excluded []string
	seen := make(map[string]bool)
	for _, message := range live.Messages {
		if !seen[message.TurnID] {
			excluded = append(excluded, message.TurnID)
			seen[message.TurnID] = true
		}
	}
	if cursor != "" {
		return sessions.options.History.Read(ctx, id, cursor, activity, excluded)
	}
	persisted, err := sessions.persisted(ctx, entry, activity, excluded)
	if err != nil {
		return empty, err
	}
	if _, err := sessions.f.snapshot(entry); err != nil {
		return empty, err
	}
	live.Messages = mergeTurns(persisted.Messages, live.Messages)
	live.NextCursor = persisted.NextCursor
	return live, nil
}

func (sessions *DesktopSessions) persisted(ctx context.Context, entry *followed, activity bool, excluded []string) (protocol.ConversationPage, error) {
	key := strconv.FormatBool(activity) + ":" + strings.Join(excluded, ",")
	f := sessions.f
	for {
		f.mu.Lock()
		if f.stopped || f.entries[entry.id] != entry {
			f.mu.Unlock()
			return protocol.ConversationPage{}, unavailable()
		}
		if entry.persisted == nil || entry.persisted.key != key {
			// Keep one bounded native history read per follower. A newer snapshot or
			// activity mode must not cancel another caller's already accepted read.
			if previous := entry.persisted; previous != nil {
				select {
				case <-previous.done:
				default:
					f.mu.Unlock()
					select {
					case <-ctx.Done():
						return protocol.ConversationPage{}, ctx.Err()
					case <-previous.done:
						continue
					}
				}
			}
			readCtx, cancel := context.WithTimeout(f.ctx, protocol.DefaultTaskRequestTimeouts.Read)
			load := &persistedRead{key: key, done: make(chan struct{}), cancel: cancel}
			entry.persisted = load
			go func() {
				defer cancel()
				page, err := sessions.options.History.Read(readCtx, entry.id, "", activity, excluded)
				f.mu.Lock()
				load.page, load.err = page, err
				if err != nil && entry.persisted == load {
					entry.persisted = nil
				}
				close(load.done)
				f.mu.Unlock()
			}()
		}
		load := entry.persisted
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return protocol.ConversationPage{}, ctx.Err()
		case <-load.done:
			return load.page, load.err
		}
	}
}

func mergeTurns(persisted, live []protocol.Message) []protocol.Message {
	groups := make([][]protocol.Message, 0)
	positions := make(map[string]int)
	for _, source := range [][]protocol.Message{persisted, live} {
		seen := make(map[string]bool)
		for _, message := range source {
			index, exists := positions[message.TurnID]
			if !exists {
				index = len(groups)
				positions[message.TurnID] = index
				groups = append(groups, nil)
			}
			if !seen[message.TurnID] {
				groups[index] = nil
				seen[message.TurnID] = true
			}
			groups[index] = append(groups[index], message)
		}
	}
	slices.SortStableFunc(groups, func(a, b []protocol.Message) int {
		difference := float64(a[0].Timestamp - b[0].Timestamp)
		if math.IsNaN(difference) || difference == 0 {
			return 0
		}
		if difference < 0 {
			return -1
		}
		return 1
	})
	messages := make([]protocol.Message, 0)
	for _, group := range groups {
		messages = append(messages, group...)
	}
	return messages
}
