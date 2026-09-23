package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

func (f *Followers) acquire(id, owner string, retain, readOnly bool) (*followed, error) {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return nil, unavailable()
	}
	if readOnly && time.Now().Before(f.retry[id]) {
		f.mu.Unlock()
		return nil, nil
	}
	entry := f.entries[id]
	var replaced *followed
	if entry != nil && entry.owner != owner {
		if entry.state == nil {
			f.mu.Unlock()
			return nil, unavailable()
		}
		replaced = entry
		f.dropLocked(entry, unavailable())
		entry = nil
	}
	created := entry == nil
	if created {
		if len(f.entries) >= 4 {
			f.mu.Unlock()
			return nil, errors.New("Too many desktop subscriptions")
		}
		entry = &followed{id: id, owner: owner, revision: -1, used: time.Now(), ready: make(chan struct{})}
		f.entries[id] = entry
	}
	entry.used = time.Now()
	if retain {
		entry.refs++
	}
	entry.readRequested = entry.readRequested || readOnly
	f.mu.Unlock()
	// References belong to their exact owner/entry. Old subscribers must reconnect
	// instead of transferring references that their cleanup can no longer release.
	if replaced != nil {
		_ = f.follow(replaced, false)
		f.closed()
	}
	if created {
		if err := f.follow(entry, true); err != nil {
			f.mu.Lock()
			f.dropLocked(entry, err)
			f.mu.Unlock()
			return nil, err
		}
		go f.watch(entry)
	}
	return entry, nil
}

func (f *Followers) watch(entry *followed) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-entry.ready:
		return
	case <-f.ctx.Done():
		return
	case <-timer.C:
		f.mu.Lock()
		dropped := f.entries[entry.id] == entry && !entry.settled && f.dropLocked(entry, snapshotUnavailable)
		if dropped && entry.readRequested {
			f.retry[entry.id] = time.Now().Add(30 * time.Second)
		}
		f.mu.Unlock()
		if dropped {
			_ = f.follow(entry, false)
		}
	}
}

// A short read timeout permits persisted data while the same subscription keeps
// waiting. Transport/owner errors and caller cancellation never take that path.
func (f *Followers) state(ctx context.Context, id, owner string, retain, readOnly bool) (*followed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, err := f.acquire(id, owner, retain, readOnly)
	if err != nil || entry == nil {
		return nil, err
	}
	return f.waitState(ctx, entry, retain, readOnly)
}

func (f *Followers) waitState(ctx context.Context, entry *followed, retain, readOnly bool) (*followed, error) {
	var timeout <-chan time.Time
	if readOnly {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-ctx.Done():
		if retain {
			f.release(entry, false)
		}
		return nil, ctx.Err()
	case <-timeout:
		return nil, nil
	case <-entry.ready:
		f.mu.Lock()
		err := entry.err
		if err == nil && (f.stopped || f.entries[entry.id] != entry) {
			err = unavailable()
		}
		f.mu.Unlock()
		if readOnly && errors.Is(err, snapshotUnavailable) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return entry, nil
	}
}

func (f *Followers) snapshot(entry *followed) (codex.DesktopState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped || f.entries[entry.id] != entry || entry.state == nil || entry.err != nil {
		return nil, unavailable()
	}
	return entry.state, nil
}
