// Package sessions follows original Desktop owners without acquiring a writer.
package sessions

import (
	"context"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/desktop"
)

type IPC interface {
	Connect(context.Context) error
	Request(context.Context, string, int, any, string) (desktop.Message, error)
	Broadcast(string, int, any, []string) error
	Close()
}

// Hooks must return without waiting for a new IPC response; the native reader
// invokes Receive synchronously. Neither callback runs under the follower lock.
type Hooks struct {
	Changed func(string)
	Closed  func()
}

type followed struct {
	id, owner              string
	revision               int64
	state                  codex.DesktopState
	used                   time.Time
	refs                   int
	ready                  chan struct{}
	settled, readRequested bool
	err                    error
	persisted              *persistedRead
}

type Followers struct {
	mu      sync.Mutex
	sendMu  sync.Mutex
	ipc     IPC
	hooks   Hooks
	ctx     context.Context
	cancel  context.CancelFunc
	entries map[string]*followed
	seen    map[string]bool
	retry   map[string]time.Time
	stopped bool
}

func unavailable() error {
	return nativeError("Original desktop owner is unavailable; refresh before continuing")
}

func nativeError(message string) error {
	code := -32600.0
	return &codex.RpcError{Message: message, Code: &code}
}

var snapshotUnavailable = nativeError("Original desktop snapshot is unavailable")

func NewFollowers(ipc IPC, hooks Hooks) *Followers {
	ctx, cancel := context.WithCancel(context.Background())
	f := &Followers{ipc: ipc, hooks: hooks, ctx: ctx, cancel: cancel, entries: make(map[string]*followed), seen: make(map[string]bool), retry: make(map[string]time.Time)}
	go f.sweep()
	return f
}

func (f *Followers) changed(id string) {
	if f.hooks.Changed != nil {
		f.hooks.Changed(id)
	}
}
func (f *Followers) closed() {
	if f.hooks.Closed != nil {
		f.hooks.Closed()
	}
}

func (f *Followers) settleLocked(entry *followed, err error) {
	entry.err = err
	if !entry.settled {
		entry.settled = true
		close(entry.ready)
	}
}

func (f *Followers) dropLocked(entry *followed, err error) bool {
	if f.entries[entry.id] != entry {
		return false
	}
	delete(f.entries, entry.id)
	if entry.persisted != nil {
		entry.persisted.cancel()
	}
	f.settleLocked(entry, err)
	return true
}

func (f *Followers) follow(entry *followed, following bool) error {
	f.sendMu.Lock()
	defer f.sendMu.Unlock()
	if following {
		f.mu.Lock()
		valid := !f.stopped && f.entries[entry.id] == entry
		f.mu.Unlock()
		if !valid {
			return unavailable()
		}
	}
	return f.ipc.Broadcast("thread-stream-following-changed", 1, map[string]any{"conversationId": entry.id, "hostId": "local", "following": following}, []string{entry.owner})
}

func (f *Followers) unfollow(id string) {
	f.mu.Lock()
	entry := f.entries[id]
	if entry != nil {
		f.dropLocked(entry, unavailable())
	}
	f.mu.Unlock()
	if entry != nil {
		_ = f.follow(entry, false)
	}
}

func (f *Followers) release(entry *followed, immediate bool) {
	f.mu.Lock()
	drop := false
	if f.entries[entry.id] == entry {
		entry.refs = max(0, entry.refs-1)
		entry.used = time.Now()
		if immediate && entry.refs == 0 {
			drop = f.dropLocked(entry, unavailable())
		}
	}
	f.mu.Unlock()
	if drop {
		_ = f.follow(entry, false)
	}
}

// isStopped reports the terminal state without taking the send lock, so a
// control path can check it before and after an asynchronous native call.
func (f *Followers) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// forget releases one subscription and removes the task from the set of tasks
// this service confirmed as desktop owned, which is what archiving does.
func (f *Followers) forget(id string) {
	f.unfollow(id)
	f.mu.Lock()
	delete(f.seen, id)
	f.mu.Unlock()
	f.changed(id)
}

func (f *Followers) detach(id string) {
	f.mu.Lock()
	entry := f.entries[id]
	f.mu.Unlock()
	if entry != nil {
		f.release(entry, false)
	}
}

func (f *Followers) Disconnected() {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return
	}
	for _, entry := range f.entries {
		f.dropLocked(entry, unavailable())
	}
	f.mu.Unlock()
	f.closed()
}

func (f *Followers) Close() {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return
	}
	f.stopped = true
	entries := make([]*followed, 0, len(f.entries))
	for _, entry := range f.entries {
		entries = append(entries, entry)
		f.dropLocked(entry, unavailable())
	}
	clear(f.retry)
	f.mu.Unlock()
	f.cancel()
	f.closed()
	for _, entry := range entries {
		_ = f.follow(entry, false)
	}
	f.ipc.Close()
}

func (f *Followers) sweep() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-f.ctx.Done():
			return
		case now := <-ticker.C:
			var expired []*followed
			f.mu.Lock()
			for _, entry := range f.entries {
				if entry.refs == 0 && now.Sub(entry.used) > 30*time.Second && f.dropLocked(entry, unavailable()) {
					expired = append(expired, entry)
				}
			}
			for id, until := range f.retry {
				if !now.Before(until) {
					delete(f.retry, id)
				}
			}
			f.mu.Unlock()
			for _, entry := range expired {
				_ = f.follow(entry, false)
			}
		}
	}
}
