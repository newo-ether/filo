package sessions

import (
	"context"
	"sync"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/protocol"
)

// A read lease protects both pending snapshots and persisted/live merging from
// another reader's release. Snapshot absence keeps only the bounded watcher.
func (sessions *DesktopSessions) readState(ctx context.Context, id, owner string, immediate bool) (*followed, func(), error) {
	noop := func() {}
	if err := ctx.Err(); err != nil {
		return nil, noop, err
	}
	entry, err := sessions.f.acquire(id, owner, true, true)
	if err != nil || entry == nil {
		return nil, noop, err
	}
	ready, err := sessions.f.waitState(ctx, entry, false, true)
	if err != nil || ready == nil {
		sessions.f.release(entry, false)
		return nil, noop, err
	}
	var once sync.Once
	return entry, func() { once.Do(func() { sessions.f.release(entry, immediate) }) }, nil
}

type History interface {
	List(context.Context, string) (protocol.SessionPage, error)
	Read(context.Context, string, string, bool, []string) (protocol.ConversationPage, error)
}

// CreatedReader can only expose the separately verified Filo-created scope.
// Being present in ordinary history never grants this ownership. A successful
// attachment returns its own release action, which is the TS detach.
type CreatedReader interface {
	Owns(context.Context, string) (bool, error)
	Read(context.Context, string, string, bool) (protocol.ConversationPage, error)
	Statuses(context.Context, []string) ([]protocol.SessionStatus, error)
	Attach(context.Context, string) (func(), error)
	Send(context.Context, string, string, string) (protocol.SendReceipt, error)
	Stop(context.Context, string, string) error
	UpdateSettings(context.Context, string, protocol.SessionSettings) error
}

// Base is the peripheral native catalog this surface forwards. None of these
// operations creates, resumes or executes a task of its own.
type Base interface {
	Create(context.Context, string, string, protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error)
	Models(context.Context) ([]protocol.Model, error)
	Rename(context.Context, string, string) (protocol.Session, error)
	Archive(context.Context, string) (string, error)
}

type Options struct {
	IPC     IPC
	History History
	Base    Base
	Created CreatedReader
	Hooks   Hooks
}
type DesktopSessions struct {
	f       *Followers
	options Options
}

func New(options Options) *DesktopSessions {
	return &DesktopSessions{f: NewFollowers(options.IPC, options.Hooks), options: options}
}

func (sessions *DesktopSessions) Receive(message desktop.Message) { sessions.f.Receive(message) }
func (sessions *DesktopSessions) Disconnected()                   { sessions.f.Disconnected() }
func (sessions *DesktopSessions) Close()                          { sessions.f.Close() }
func (sessions *DesktopSessions) SourceClosed()                   { sessions.f.closed() }
func (sessions *DesktopSessions) SourceChanged(id string) {
	sessions.f.mu.Lock()
	entry := sessions.f.entries[id]
	sessions.f.mu.Unlock()
	if entry == nil {
		sessions.f.changed(id)
	}
}

func (sessions *DesktopSessions) created(ctx context.Context, id string) (bool, error) {
	sessions.f.mu.Lock()
	seen := sessions.f.seen[id]
	sessions.f.mu.Unlock()
	if seen || sessions.options.Created == nil {
		return false, nil
	}
	return sessions.options.Created.Owns(ctx, id)
}

// A successful attachment returns its exact release action. A late cleanup from
// a disconnected client cannot release a replacement owner's subscription.
func (sessions *DesktopSessions) Attach(ctx context.Context, id string) (func(), error) {
	owner, err := sessions.f.owner(ctx, id)
	if err != nil {
		return nil, err
	}
	if owner == "" {
		created, err := sessions.created(ctx, id)
		if err != nil {
			return nil, err
		}
		if created {
			return sessions.options.Created.Attach(ctx, id)
		}
		_, err = sessions.options.History.Read(ctx, id, "", false, nil)
		return func() {}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, err := sessions.f.acquire(id, owner, true, true)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return func() {}, nil
	}
	if _, err := sessions.f.waitState(ctx, entry, true, true); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { sessions.f.release(entry, false) }) }, nil
}

func notLoaded(page protocol.ConversationPage) protocol.ConversationPage {
	runtime := protocol.Runtime{}
	if page.Runtime != nil {
		runtime = *page.Runtime
	}
	runtime.Status = "notLoaded"
	runtime.ActiveTurnID = nil
	page.Runtime = &runtime
	return page
}
