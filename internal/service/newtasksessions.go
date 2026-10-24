package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
)

const (
	// taskSessionsListedBound limits each status query to a bounded fan-out.
	taskSessionsListedBound = 30
	// taskSessionsShuttingDown refuses operations while the helper drains.
	// Admission checks use an RPC error; post-acquisition failures use a plain error.
	taskSessionsShuttingDown = "Filo user helper is shutting down"
)

// TaskSessionsMetadata is the peripheral native surface this adapter reads and
// mutates. *codex.NativeMetadata satisfies it, and it holds no create, resume,
// turn or stop operation of its own.
type TaskSessionsMetadata interface {
	Models() ([]protocol.Model, error)
	Rename(id, name string) (protocol.Session, error)
	Archive(id string) (string, error)
	PrepareShutdown() error
}

// TaskSessionsHistory is the read-only native history this adapter falls back to
// whenever no admitted connection owns a task. *history.NativeHistory satisfies
// it and cannot acquire an owner or launch a native process.
type TaskSessionsHistory interface {
	List(ctx context.Context, cursor string) (protocol.SessionPage, error)
	Read(ctx context.Context, id, cursor string, activity bool, excludedTurns []string) (protocol.ConversationPage, error)
}

// TaskSessionsConnection is one admitted native connection as this adapter drives
// it: the JSONL transport it requests on, the transport whose notifications and
// close it dispatches from, and the close that ends only this client. Nothing
// here signals a native process.
//
// *codex.CodexHost exposes its transport as a field, so the production wiring
// wraps it in executorConnection instead of widening the codex port.
type TaskSessionsConnection interface {
	Transport() *codex.Rpc
	Close()
}

// TaskSessionsFactory opens private connections for one leased user helper.
// *TaskExecutorFactory is adapted by ExecutorConnections.
type TaskSessionsFactory interface {
	Owns(taskID string) (bool, error)
	Open(ctx context.Context, taskID *string) (TaskSessionsConnection, error)
	Follow(ctx context.Context, taskID string) (TaskSessionsConnection, error)
	Close()
}

// TaskSessions is the scoped task adapter of one leased user helper. Native
// history is always read-only; only an explicit mutation may start a scoped task
// executor, and every request stays on the same authenticated host that already
// owns the admitted task.
//
// SetChangedHandler reports task changes. The adapter dispatches each connection's
// notifications to its consumers and protects shared maps from concurrent HTTP
// handlers. Operations use the caller's context where supported by native ports.
type TaskSessions struct {
	metadata TaskSessionsMetadata
	history  TaskSessionsHistory
	factory  TaskSessionsFactory

	mu           sync.Mutex
	entries      map[string]*taskSessionsEntry
	opening      map[string]*taskSessionsOpening
	draining     bool
	writes       int
	changed      func(string)
	accessSecret string
}

// taskSessionsEntry is one admitted connection and the live bookkeeping of the
// single task it serves.
type taskSessionsEntry struct {
	connection     TaskSessionsConnection
	sessions       *codex.NativeTaskSession
	activity       *codex.TaskActivity
	native         *nativeTaskSessionsConnection
	operation      sync.Mutex
	accessID       string
	accessEpoch    uint64
	executionOwner *codex.Rpc
}

// taskSessionsOpening is one in-flight acquisition, shared by every caller that
// asked for the same task while it was opening.
type taskSessionsOpening struct {
	done  chan struct{}
	entry *taskSessionsEntry
	err   error
}

// ExecutorConnections adapts the concrete executor factory's return values to
// the connection-level interface used by TaskSessions.
func ExecutorConnections(factory *TaskExecutorFactory) TaskSessionsFactory {
	return executorConnections{factory: factory}
}

type executorConnections struct {
	factory *TaskExecutorFactory
}

func (adapter executorConnections) Owns(taskID string) (bool, error) {
	return adapter.factory.Owns(taskID)
}

func (adapter executorConnections) Open(ctx context.Context, taskID *string) (TaskSessionsConnection, error) {
	return admittedConnection(adapter.factory.Open(ctx, taskID))
}

func (adapter executorConnections) Follow(ctx context.Context, taskID string) (TaskSessionsConnection, error) {
	return admittedConnection(adapter.factory.Follow(ctx, taskID))
}

func (adapter executorConnections) Close() { adapter.factory.Close() }

// executorConnection is the connection-level view of one admitted codex host.
type executorConnection struct {
	host *codex.CodexHost
}

func (connection executorConnection) Transport() *codex.Rpc { return connection.host.Rpc }
func (connection executorConnection) Close()                { connection.host.Close() }

// admittedConnection keeps a missing host a nil interface. Returning the typed nil
// instead would look like an admitted connection to every caller of this adapter.
func admittedConnection(host *codex.CodexHost, err error) (TaskSessionsConnection, error) {
	if host == nil {
		return nil, err
	}
	return executorConnection{host: host}, err
}

// NewTaskSessions builds the scoped adapter over the production collaborators.
func NewTaskSessions(metadata TaskSessionsMetadata, nativeHistory TaskSessionsHistory,
	factory *TaskExecutorFactory) *TaskSessions {
	return NewTaskSessionsWith(metadata, nativeHistory, ExecutorConnections(factory))
}

// NewTaskSessionsWith accepts explicit ports so tests can replace native executors
// with scripted connections.
func NewTaskSessionsWith(metadata TaskSessionsMetadata, nativeHistory TaskSessionsHistory,
	factory TaskSessionsFactory) *TaskSessions {
	secret, err := executorSecret()
	if err != nil {
		panic("Cannot initialize task access identity")
	}
	return &TaskSessions{
		accessSecret: secret,
		metadata:     metadata,
		history:      nativeHistory,
		factory:      factory,
		entries:      map[string]*taskSessionsEntry{},
		opening:      map[string]*taskSessionsOpening{},
	}
}

// The scoped adapter is the whole session surface of a leased user helper: the
// bounded session API, the listed status source and the safe shutdown hook.
var (
	_ SessionAPI   = (*TaskSessions)(nil)
	_ StatusSource = (*TaskSessions)(nil)
	_ Shutdowner   = (*TaskSessions)(nil)
)

// SetChangedHandler installs the callback that reports the exact task whose
// native presentation changed.
func (s *TaskSessions) SetChangedHandler(callback func(string)) {
	s.mu.Lock()
	s.changed = callback
	s.mu.Unlock()
}

// Owns reports whether this helper durably created one task, which is the only
// admission proof that may start a writer.
func (s *TaskSessions) Owns(id string) (bool, error) { return s.factory.Owns(id) }

// List reads the read-only native catalog. Listing never opens a connection.
func (s *TaskSessions) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	return s.history.List(ctx, cursor)
}

// Models reads the native catalog through a port with no context parameter.
func (s *TaskSessions) Models(context.Context) ([]protocol.Model, error) {
	return s.metadata.Models()
}

// Create starts one native task on a brand new admitted connection, applies the
// optional drafted settings, and runs its first turn in the same mutation, so a
// created task is never left empty and never has a window where it is
// unpersisted. It is a mutation: browsing never creates a writer.
func (s *TaskSessions) Create(ctx context.Context, text, clientID string,
	settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	var created protocol.Session
	var receipt protocol.SendReceipt
	err := s.mutation(func() error {
		if err := protocol.ValidateScope(ctx); err != nil {
			return err
		}
		recordCreateStage(ctx, createStageHelperAllocationStarted, nil)
		connection, err := s.factory.Open(ctx, nil)
		if err != nil {
			recordCreateStage(ctx, createStageHelperAllocationFailed, err)
			return err
		}
		if connection == nil {
			// Creation cannot proceed without an admitted connection.
			err := errors.New("Filo native executor is unavailable")
			recordCreateStage(ctx, createStageHelperAllocationFailed, err)
			return err
		}
		recordCreateStage(ctx, createStageHelperAllocationCompleted, nil)
		if s.isDraining() {
			connection.Close()
			return drainingError()
		}
		native := s.taskNative(connection)
		native.ctx = ctx
		sessions := codex.NewNativeTaskSession(native)
		if err := protocol.ValidateScope(ctx); err != nil {
			connection.Close()
			return err
		}
		recordCreateStage(ctx, createStageHelperNativeStartStarted, nil)
		session, err := sessions.Create()
		if err != nil {
			recordCreateStage(ctx, createStageHelperNativeStartFailed, err)
			connection.Close()
			return err
		}
		recordCreateStage(ctx, createStageHelperProvenanceStarted, nil)
		owned, err := s.factory.Owns(session.ID)
		if err != nil {
			recordCreateStage(ctx, createStageHelperProvenanceFailed, err)
			connection.Close()
			return err
		}
		if !owned {
			err := errors.New("Native creation provenance is unconfirmed")
			recordCreateStage(ctx, createStageHelperProvenanceFailed, err)
			connection.Close()
			return err
		}
		recordCreateStage(ctx, createStageHelperProvenanceVerified, nil)
		entry := s.register(session.ID, connection, sessions, native)
		entry.operation.Lock()
		defer func() {
			native.mu.Lock()
			native.ctx = nil
			native.mu.Unlock()
			entry.operation.Unlock()
		}()
		s.mu.Lock()
		if s.draining {
			s.mu.Unlock()
			connection.Close()
			return drainingError()
		}
		s.entries[session.ID] = entry
		s.mu.Unlock()
		// A drafted settings patch lands between the native task and its first
		// turn, so the first turn already runs with the settings the client
		// chose. A refused patch ends the creation before any turn exists: the
		// never-started task is then left unpersisted by the native side, which
		// is exactly the state a rejected creation must leave behind.
		if settings.Model.Known || settings.Effort.Known || settings.ServiceTier.Known {
			if err := protocol.ValidateScope(ctx); err != nil {
				connection.Close()
				return err
			}
			if err := entry.sessions.UpdateSettings(session.ID, settings); err != nil {
				connection.Close()
				return err
			}
		}
		created = session
		// The first turn runs on the same admitted connection right after the
		// native task was created, so the thread is materialized before this
		// mutation returns and no empty, unpersisted window remains.
		input := []any{map[string]any{"type": "text", "text": protocol.Text(text), "text_elements": []any{}}}
		if err := protocol.ValidateScope(ctx); err != nil {
			connection.Close()
			return err
		}
		value, err := entry.sessions.StartTurn(session.ID, input, clientID)
		if err != nil {
			return err
		}
		fields, _ := nativejson.Fields(value)
		turn, _ := nativejson.Fields(fields["turn"])
		turnID, ok := nativejson.AsText(turn["id"])
		if !ok || turnID == "" {
			return errors.New("Native send outcome is unconfirmed")
		}
		receipt = protocol.SendReceipt{TurnID: turnID, ClientID: clientID}
		return nil
	})
	if err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	return created, receipt, nil
}

// Attach reads one task for a streaming subscriber. Connections remain in the
// entry table until the helper closes; a disconnected subscriber must not end a
// connection another subscriber still uses, so the release action is empty.
func (s *TaskSessions) Attach(ctx context.Context, id string) (func(), error) {
	if _, err := s.Read(ctx, id, "", false, nil); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Read reads one conversation page. A browsing read never starts a writer: an
// owned task is followed on its existing connection, and every other task comes
// from the read-only history. Native history stays authoritative, and the live
// buffer only bridges the window before a newly accepted turn is published.
func (s *TaskSessions) Read(ctx context.Context, id, cursor string, activity bool,
	excludedTurns []string) (protocol.ConversationPage, error) {
	entry, err := s.acquire(ctx, id, false)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if entry == nil {
		return s.history.Read(ctx, id, cursor, activity, excludedTurns)
	}
	snapshot, err := entry.sessions.Snapshot(id)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if !snapshot.Materialized && cursor == "" {
		return snapshot.Page, nil
	}
	page, readErr := s.history.Read(ctx, id, cursor, activity, excludedTurns)
	if readErr != nil {
		// A newly accepted native turn can precede its asynchronous catalog
		// publication. Only that exact owned task's missing row is bridged; no
		// storage or protocol error is hidden.
		var pending *history.NativeTaskNotIndexed
		if cursor != "" || !errors.As(readErr, &pending) || pending.TaskID != id {
			return protocol.ConversationPage{}, readErr
		}
		page = snapshot.Page
	}
	mergeTaskMetadata(&page, snapshot.Page)
	currentTurn := snapshot.TurnID
	if cursor != "" || (currentTurn != nil && containsTurn(excludedTurns, *currentTurn)) {
		return page, nil
	}
	// The activity channel is an opt-in addition to a page, so a caller that did
	// not ask for it still receives the persisted messages unchanged.
	page.Messages = entry.activity.Merge(page.Messages, currentTurn, activity,
		snapshot.Page.Runtime == nil || snapshot.Page.Runtime.ActiveTurnID == nil)
	return page, nil
}

// Send accepts one user message on an admitted task: it steers the active turn
// when the native runtime reports one, and otherwise starts the next turn. An
// unconfirmed native outcome is reported as such and is never replayed onto
// another writer.
func (s *TaskSessions) Send(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	var receipt protocol.SendReceipt
	err := s.mutation(func() error {
		entry, err := s.acquire(ctx, id, true)
		if err != nil {
			return err
		}
		if entry == nil {
			return notReady()
		}
		release, err := s.bindTaskMutation(ctx, id, entry)
		if err != nil {
			return err
		}
		defer release()
		snapshot, err := entry.sessions.Snapshot(id)
		if err != nil {
			return err
		}
		runtime := snapshot.Page.Runtime
		if runtime == nil {
			// A missing runtime cannot prove that the native task is ready.
			return notReady()
		}
		input := []any{map[string]any{"type": "text", "text": protocol.Text(text), "text_elements": []any{}}}
		if runtime.Status == "active" && runtime.ActiveTurnID != nil {
			value, err := entry.native.Request("turn/steer", map[string]any{
				"threadId": id, "input": input, "clientUserMessageId": clientID,
				"expectedTurnId": *runtime.ActiveTurnID,
			})
			if err != nil {
				return err
			}
			fields, _ := nativejson.Fields(value)
			turnID, ok := nativejson.AsText(fields["turnId"])
			if !ok || turnID == "" {
				return errors.New("Native steer outcome is unconfirmed")
			}
			receipt = protocol.SendReceipt{TurnID: turnID, ClientID: clientID}
			return nil
		}
		if runtime.Status != "idle" {
			return notReady()
		}
		value, err := entry.sessions.StartTurn(id, input, clientID)
		if err != nil {
			return err
		}
		fields, _ := nativejson.Fields(value)
		turn, _ := nativejson.Fields(fields["turn"])
		turnID, ok := nativejson.AsText(turn["id"])
		if !ok || turnID == "" {
			return errors.New("Native send outcome is unconfirmed")
		}
		receipt = protocol.SendReceipt{TurnID: turnID, ClientID: clientID}
		return nil
	})
	if err != nil {
		return protocol.SendReceipt{}, err
	}
	return receipt, nil
}

// UpdateSettings applies one next-turn settings patch on the exact admitted
// connection.
func (s *TaskSessions) UpdateSettings(ctx context.Context, id string,
	settings protocol.SessionSettings) error {
	return s.mutation(func() error {
		entry, err := s.acquire(ctx, id, true)
		if err != nil {
			return err
		}
		if entry == nil {
			return notReady()
		}
		release, err := s.bindTaskMutation(ctx, id, entry)
		if err != nil {
			return err
		}
		defer release()
		return entry.sessions.UpdateSettings(id, settings)
	})
}

// SetModel applies one model-only settings patch.
func (s *TaskSessions) SetModel(ctx context.Context, id, model string) error {
	return s.UpdateSettings(ctx, id, protocol.SessionSettings{Model: protocol.Known(model)})
}

// Stop interrupts one exact active turn. A turn that already changed is refused
// instead of being interrupted after the fact.
func (s *TaskSessions) Stop(ctx context.Context, id, turnID string) error {
	return s.mutation(func() error {
		entry, err := s.acquire(ctx, id, false)
		if err != nil {
			return err
		}
		if entry == nil {
			return activeTurnChanged()
		}
		release, err := s.bindTaskMutation(ctx, id, entry)
		if err != nil {
			return err
		}
		defer release()
		snapshot, err := entry.sessions.Snapshot(id)
		if err != nil {
			return err
		}
		runtime := snapshot.Page.Runtime
		if runtime == nil || runtime.ActiveTurnID == nil || *runtime.ActiveTurnID != turnID {
			return activeTurnChanged()
		}
		return entry.sessions.Stop(id, turnID)
	})
}

// Rename renames one task through the peripheral native metadata surface.
func (s *TaskSessions) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	var renamed protocol.Session
	err := s.mutation(func() error {
		session, err := s.metadata.Rename(id, name)
		if err != nil {
			return err
		}
		renamed = session
		return nil
	})
	if err != nil {
		return protocol.Session{}, err
	}
	return renamed, nil
}

// Archive archives one task on its admitted connection when it has one, and
// through the peripheral metadata surface otherwise.
func (s *TaskSessions) Archive(ctx context.Context, id string) (string, error) {
	var cwd string
	err := s.mutation(func() error {
		entry, err := s.acquire(ctx, id, false)
		if err != nil {
			return err
		}
		if entry == nil {
			value, err := s.metadata.Archive(id)
			if err != nil {
				return err
			}
			cwd = value
			return nil
		}
		value, err := entry.sessions.ArchiveSession(id)
		if err != nil {
			return err
		}
		cwd = value
		return nil
	})
	if err != nil {
		return "", err
	}
	return cwd, nil
}

// PrepareShutdown fences new work, requires every in-flight native operation to
// have finished, and then closes this adapter. A helper that cannot stop leaves
// every accepted task untouched.
func (s *TaskSessions) PrepareShutdown(context.Context) error {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return shuttingDown()
	}
	s.draining = true
	if s.writes != 0 {
		s.draining = false
		s.mu.Unlock()
		return pendingOperation()
	}
	s.mu.Unlock()
	if err := s.metadata.PrepareShutdown(); err != nil {
		s.mu.Lock()
		s.draining = false
		s.mu.Unlock()
		return err
	}
	s.Close()
	return nil
}

// Close fences new work, closes the factory and ends every admitted connection.
// It never signals a native process, so accepted native work keeps running. The
// entry table is emptied before a connection is closed, because the close
// callback of a connection can only ever report a task this adapter already
// forgets.
func (s *TaskSessions) Close() {
	s.mu.Lock()
	s.draining = true
	entries := make([]*taskSessionsEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.entries = map[string]*taskSessionsEntry{}
	s.mu.Unlock()
	s.factory.Close()
	for _, entry := range entries {
		entry.connection.Close()
	}
}

// mutation refuses new work while the helper drains and counts one in-flight
// native operation, which a shutdown must not race.
func (s *TaskSessions) mutation(operation func() error) error {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return shuttingDown()
	}
	s.writes++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.writes--
		s.mu.Unlock()
	}()
	return operation()
}

// register wires one admitted connection to the live bookkeeping of one task. The
// entry is inserted by its caller only after the task is attached, so the entry
// table never publishes a connection that failed to admit its task.
func (s *TaskSessions) register(id string, connection TaskSessionsConnection,
	sessions *codex.NativeTaskSession, native *nativeTaskSessionsConnection) *taskSessionsEntry {
	activity := codex.NewTaskActivity(id)
	accessID, err := executorSecret()
	if err != nil {
		panic("Cannot initialize task connection identity")
	}
	entry := &taskSessionsEntry{connection: connection, sessions: sessions, activity: activity,
		native: native, accessID: accessID, executionOwner: connection.Transport()}
	s.registerTaskAccess(id, entry)
	// Dispatch the single notification slot to sessions, then activity.
	connection.Transport().SetHandlers(nil, func(packet map[string]any) {
		method, _ := packet["method"].(string)
		if observer, ok := connection.(interface{ Observe(string, any) }); ok {
			observer.Observe(method, packet["params"])
		}
		s.observeTaskAccess(id, entry, method, packet["params"])
		sessions.Receive(method, packet["params"])
		activity.Receive(method, packet["params"])
	})
	sessions.SetChangedHandler(func(changed string) {
		if changed == id {
			s.notifyChanged(id)
		}
	})
	connection.Transport().SetClosedHandler(func(error) {
		s.mu.Lock()
		if s.entries[id] == entry {
			delete(s.entries, id)
		}
		s.mu.Unlock()
		sessions.Disconnected()
		s.notifyChanged(id)
	})
	return entry
}

// notifyChanged reports one task to the installed callback outside every lock.
func (s *TaskSessions) notifyChanged(id string) {
	s.mu.Lock()
	callback := s.changed
	s.mu.Unlock()
	if callback != nil {
		callback(id)
	}
}

// acquire returns the admitted entry of one task, opening it when necessary. A
// write acquisition may start a scoped executor, while a read acquisition only
// follows an executor that already exists. Concurrent acquisitions of the same
// task share one attempt, and only a write acquisition retries after a shared
// attempt that ended without a connection.
func (s *TaskSessions) acquire(ctx context.Context, id string, write bool) (*taskSessionsEntry, error) {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return nil, shuttingDown()
	}
	if current := s.entries[id]; current != nil {
		s.mu.Unlock()
		return current, nil
	}
	if protocol.AccessBinding(ctx) != "" {
		s.mu.Unlock()
		return nil, taskAccessChanged()
	}
	if pending := s.opening[id]; pending != nil {
		s.mu.Unlock()
		<-pending.done
		if pending.err != nil {
			return nil, pending.err
		}
		if pending.entry != nil {
			return pending.entry, nil
		}
		if write {
			return s.acquire(ctx, id, true)
		}
		return nil, nil
	}
	pending := &taskSessionsOpening{done: make(chan struct{})}
	s.opening[id] = pending
	s.mu.Unlock()
	entry, err := s.open(ctx, id, write)
	s.mu.Lock()
	pending.entry, pending.err = entry, err
	if s.opening[id] == pending {
		delete(s.opening, id)
	}
	s.mu.Unlock()
	close(pending.done)
	return entry, err
}

// open admits one task on a private connection. The connection is published only
// after the task is attached and only while the helper does not drain, so a
// shutdown can never leave a half-admitted connection in the entry table.
func (s *TaskSessions) open(ctx context.Context, id string, write bool) (*taskSessionsEntry, error) {
	var connection TaskSessionsConnection
	var err error
	if write {
		if sessions.IdleRecoveryAllowed(ctx, id) {
			page, readErr := s.history.Read(ctx, id, "", false, nil)
			if readErr != nil {
				return nil, readErr
			}
			if !sessions.RecoverableRuntime(page.Runtime) {
				return nil, notReady()
			}
		}
		connection, err = s.factory.Open(ctx, &id)
	} else {
		connection, err = s.factory.Follow(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, nil
	}
	if s.isDraining() {
		connection.Close()
		return nil, drainingError()
	}
	native := s.taskNative(connection)
	sessions := codex.NewNativeTaskSession(native)
	entry := s.register(id, connection, sessions, native)
	// This is the same authenticated host that already owns this admitted task,
	// never a browsing-time writer.
	if err := sessions.Attach(id); err != nil {
		connection.Close()
		return nil, fmt.Errorf("Cannot attach native task %s; it may have unloaded: %w", id, err)
	}
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		connection.Close()
		return nil, drainingError()
	}
	s.entries[id] = entry
	s.mu.Unlock()
	return entry, nil
}

func (s *TaskSessions) isDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// shuttingDown is the RPC refusal of a draining helper.
func shuttingDown() *codex.RpcError {
	return &codex.RpcError{Message: taskSessionsShuttingDown, Code: f64(-32600)}
}

// drainingError refuses an acquisition after the factory handed it a connection.
func drainingError() error { return errors.New(taskSessionsShuttingDown) }

// notReady is the refusal of a message the native runtime cannot accept.
func notReady() *codex.RpcError {
	return &codex.RpcError{Message: "Native task is not ready", Code: f64(-32600)}
}

// activeTurnChanged is the refusal of a stop that names a turn the native runtime
// no longer reports.
func activeTurnChanged() *codex.RpcError {
	return &codex.RpcError{Message: "Native active turn changed", Code: f64(-32600)}
}

// pendingOperation is the refusal of a shutdown while a native mutation runs.
func pendingOperation() *codex.RpcError {
	return &codex.RpcError{Message: "A native operation is still pending", Code: f64(-32600)}
}

// containsTurn reports whether one turn identity is excluded from a page.
func containsTurn(turns []string, id string) bool {
	for _, turn := range turns {
		if turn == id {
			return true
		}
	}
	return false
}
