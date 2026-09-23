package codex

import (
	"errors"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// hostConnection is the request surface one task session needs: the default
// thirty second request plus the bounded native creation budget. *Rpc satisfies
// both.
type hostConnection interface {
	hostReader
	RequestTimeout(method string, params any, timeout time.Duration) (any, error)
}

// nativeSessionSettings is the native next-turn presentation of one attached
// task, as read back from native responses and notifications. An absent key and
// an explicit null are both unknown for the model and the effort, because the
// native wildcard that consumed them treats null the same way; an explicit null
// service tier is the native default and is a known value.
type nativeSessionSettings struct {
	Model       *string
	Effort      *string
	ServiceTier protocol.Optional[*string]
}

// taskUsage is the bounded token accounting of one attached task.
type taskUsage struct {
	TotalTokens   *float64
	ContextWindow *float64
}

// taskAttachment is one attach attempt, shared by every caller that asked for
// the same task while it was resuming.
type taskAttachment struct {
	done chan struct{}
	err  error
}

// NativeTaskSession keeps the attached-task bookkeeping of one admitted native
// connection: which tasks are attached, their last reported settings and their
// last reported token usage. Operations stay on the exact native connection
// admitted by the task executor, and it never creates, resumes or executes a
// task outside that connection.
//
// Divergence from the TypeScript EventEmitter: notifications and the close cause
// arrive through Receive and Disconnected, because a Go transport has exactly one
// notification handler and its owner dispatches from it.
type NativeTaskSession struct {
	rpc      hostConnection
	metadata *NativeMetadata
	mu       sync.Mutex
	attached map[string]*taskAttachment
	settings map[string]nativeSessionSettings
	usage    map[string]taskUsage
	changed  func(string)
	closed   func()
}

// NewNativeTaskSession builds the session bookkeeping of one native connection.
func NewNativeTaskSession(rpc hostConnection) *NativeTaskSession {
	return &NativeTaskSession{rpc: rpc, metadata: NewNativeMetadata(rpc),
		attached: map[string]*taskAttachment{}, settings: map[string]nativeSessionSettings{},
		usage: map[string]taskUsage{}}
}

// SetChangedHandler installs the callback that reports the exact task whose
// native presentation changed.
func (s *NativeTaskSession) SetChangedHandler(callback func(string)) {
	s.mu.Lock()
	s.changed = callback
	s.mu.Unlock()
}

// SetClosedHandler installs the callback for a lost native connection.
func (s *NativeTaskSession) SetClosedHandler(callback func()) {
	s.mu.Lock()
	s.closed = callback
	s.mu.Unlock()
}

// Receive folds one native notification into the attached-task bookkeeping. Only
// an attached identity is observed, and every observed notification reports that
// task as changed, whether or not it carried a value this session retains.
//
// Divergence from the TypeScript `receive(event: RpcNotification)`: the method and
// the decoded params are passed separately, which is the Go form of one packet. A
// threadSettings value that is present but not an object is ignored instead of
// clearing the recorded settings, which is a malformed-payload difference only.
func (s *NativeTaskSession) Receive(method string, params any) {
	fields, ok := nativejson.Fields(params)
	if !ok {
		return
	}
	id, _ := nativejson.AsText(fields["threadId"])
	if id == "" {
		thread, _ := nativejson.Fields(fields["thread"])
		id, _ = nativejson.AsText(thread["id"])
	}
	if id == "" {
		return
	}
	s.mu.Lock()
	if _, attached := s.attached[id]; !attached {
		s.mu.Unlock()
		return
	}
	switch method {
	case "thread/tokenUsage/updated":
		s.usage[id] = readTaskUsage(fields["tokenUsage"])
	case "thread/settings/updated":
		if threadSettings, isObject := nativejson.Fields(fields["threadSettings"]); isObject {
			s.settings[id] = readNativeSettings(threadSettings, "effort")
		}
	case "thread/closed":
		delete(s.attached, id)
		delete(s.settings, id)
		delete(s.usage, id)
	}
	changed := s.changed
	s.mu.Unlock()
	if changed != nil {
		changed(id)
	}
}

// Disconnected forgets every attached task of a lost native connection.
func (s *NativeTaskSession) Disconnected() {
	s.mu.Lock()
	s.attached = map[string]*taskAttachment{}
	s.settings = map[string]nativeSessionSettings{}
	s.usage = map[string]taskUsage{}
	closed := s.closed
	s.mu.Unlock()
	if closed != nil {
		closed()
	}
}

// Attach resumes one native task and records the settings that response reported.
// A second attach for the same task shares the first attempt, and a failed
// attempt is forgotten so a later caller can retry it.
func (s *NativeTaskSession) Attach(threadID string) error {
	s.mu.Lock()
	if existing := s.attached[threadID]; existing != nil {
		s.mu.Unlock()
		<-existing.done
		return existing.err
	}
	attachment := &taskAttachment{done: make(chan struct{})}
	s.attached[threadID] = attachment
	s.mu.Unlock()
	value, err := s.rpc.Request("thread/resume", map[string]any{"threadId": threadID, "excludeTurns": true})
	s.mu.Lock()
	if err == nil {
		s.settings[threadID] = readNativeSettings(value, "reasoningEffort")
	}
	attachment.err = err
	if err != nil && s.attached[threadID] == attachment {
		delete(s.attached, threadID)
	}
	s.mu.Unlock()
	close(attachment.done)
	return err
}

// Create starts one native task on this admitted connection and records it as
// attached. An omitted cwd inherits this admitted native executor default, never
// the viewed task or plugin directory.
func (s *NativeTaskSession) Create() (protocol.Session, error) {
	result, err := s.rpc.RequestTimeout("thread/start", map[string]any{},
		protocol.DefaultTaskRequestTimeouts.Create)
	if err != nil {
		return protocol.Session{}, err
	}
	fields, _ := nativejson.Fields(result)
	threadFields, isObject := nativejson.Fields(fields["thread"])
	if !isObject {
		return protocol.Session{}, errors.New("Invalid native task identity")
	}
	var thread metadataThread
	if err := decodeMetadata(threadFields, &thread); err != nil {
		return protocol.Session{}, err
	}
	s.mu.Lock()
	s.attached[thread.ID] = settledAttachment()
	s.settings[thread.ID] = readNativeSettings(result, "reasoningEffort")
	s.mu.Unlock()
	return thread.session(), nil
}

// ArchiveSession archives one owned native task and forgets its attachment.
func (s *NativeTaskSession) ArchiveSession(id string) (string, error) {
	cwd, err := s.metadata.Archive(id)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	delete(s.attached, id)
	delete(s.settings, id)
	delete(s.usage, id)
	changed := s.changed
	s.mu.Unlock()
	if changed != nil {
		changed(id)
	}
	return cwd, nil
}

// Models reads the native model catalog of this exact connection.
func (s *NativeTaskSession) Models() ([]protocol.Model, error) { return s.metadata.Models() }

// NativeTaskSnapshot is the materialized runtime view of one native task, read
// from the already owned host without introducing a second history cursor.
type NativeTaskSnapshot struct {
	Page         protocol.ConversationPage
	Materialized bool
	// TurnID is the current native turn identity, absent when the native task
	// reports no turn at all.
	TurnID *string
}

// Snapshot reads the native task identity, its materialized state and the runtime
// presentation of its current turn. An identity that does not answer for the
// requested task, and a task state that cannot be trusted, are both refused.
func (s *NativeTaskSession) Snapshot(threadID string) (NativeTaskSnapshot, error) {
	result, err := s.rpc.Request("thread/read", map[string]any{"threadId": threadID, "includeTurns": false})
	if err != nil {
		return NativeTaskSnapshot{}, err
	}
	fields, _ := nativejson.Fields(result)
	thread, isObject := nativejson.Fields(fields["thread"])
	threadIDNative, _ := nativejson.AsText(thread["id"])
	if !isObject || threadIDNative != threadID {
		return NativeTaskSnapshot{}, errors.New("Native task identity mismatch")
	}
	stateResult, err := s.rpc.Request("filo/task/state", map[string]any{"threadId": threadID})
	if err != nil {
		return NativeTaskSnapshot{}, err
	}
	state, isObject := nativejson.Fields(stateResult)
	if !isObject {
		return NativeTaskSnapshot{}, errors.New("Invalid executor task state")
	}
	materialized, hasMaterialized := state["materialized"].(bool)
	hasUser, hasHasUser := state["hasUser"].(bool)
	turn, _ := nativejson.Fields(state["turn"])
	turnID, hasTurnID := nativejson.AsText(turn["id"])
	turnStatus, hasTurnStatus := nativejson.AsText(turn["status"])
	presentTurn := state["turn"] != nil
	if !hasMaterialized || !hasHasUser || (presentTurn && (!hasTurnID || !hasTurnStatus)) {
		return NativeTaskSnapshot{}, errors.New("Invalid executor task state")
	}
	statusFields, _ := nativejson.Fields(thread["status"])
	status := "notLoaded"
	if text, ok := statusFields["type"].(string); ok {
		status = text
	}
	s.mu.Lock()
	settings, hasSettings := s.settings[threadID]
	usage, hasUsage := s.usage[threadID]
	s.mu.Unlock()
	runtime := protocol.Runtime{Status: status, CompletedTurnID: protocol.Known[*string](nil)}
	if status == "active" && turnStatus == "inProgress" {
		runtime.ActiveTurnID = &turnID
	}
	if turnStatus == "completed" {
		runtime.CompletedTurnID = protocol.Known(&turnID)
	}
	if hasSettings && settings.Model != nil {
		runtime.Model = settings.Model
	} else if text, ok := thread["model"].(string); ok {
		runtime.Model = &text
	}
	if hasSettings && settings.Effort != nil {
		runtime.Effort = protocol.Known(settings.Effort)
	} else {
		runtime.Effort = optionalNullableText(thread, "reasoningEffort")
	}
	if hasSettings && settings.ServiceTier.Known {
		runtime.ServiceTier = settings.ServiceTier
		runtime.ServiceTierKnown = protocol.Known(true)
	}
	if hasUsage {
		runtime.ContextTokens = usage.TotalTokens
		runtime.ContextWindow = usage.ContextWindow
	}
	runtime.ActiveTurnHasUserMessage = protocol.Known(hasUser)
	snapshot := NativeTaskSnapshot{Materialized: materialized,
		Page: protocol.ConversationPage{
			Messages:   []protocol.Message{},
			NextCursor: nil,
			Queued:     []protocol.QueuedInput{},
			Runtime:    &runtime,
		}}
	if presentTurn {
		snapshot.TurnID = &turnID
	}
	return snapshot, nil
}

// UpdateSettings validates one desired patch against the native model catalog and
// then sends it. Values are read back from native responses and notifications
// and are never copied from the desired patch.
func (s *NativeTaskSession) UpdateSettings(threadID string, settings protocol.SessionSettings) error {
	models, err := s.Models()
	if err != nil {
		return err
	}
	var currentModel *string
	if !settings.Model.Known || settings.Model.Value == "" {
		result, err := s.rpc.Request("thread/read", map[string]any{"threadId": threadID})
		if err != nil {
			return err
		}
		fields, _ := nativejson.Fields(result)
		thread, _ := nativejson.Fields(fields["thread"])
		if text, ok := thread["model"].(string); ok {
			currentModel = &text
		}
	}
	if err := ValidateSettings(settings, models, currentModel); err != nil {
		return err
	}
	if err := s.Attach(threadID); err != nil {
		return err
	}
	params := map[string]any{"threadId": threadID}
	if settings.Model.Known {
		params["model"] = settings.Model.Value
	}
	if settings.Effort.Known {
		params["effort"] = settings.Effort.Value
	}
	if settings.ServiceTier.Known {
		params["serviceTier"] = settings.ServiceTier.Value
	}
	if _, err := s.rpc.Request("thread/settings/update", params); err != nil {
		return err
	}
	s.mu.Lock()
	changed := s.changed
	s.mu.Unlock()
	if changed != nil {
		changed(threadID)
	}
	return nil
}

// Stop interrupts one native turn of this exact connection.
func (s *NativeTaskSession) Stop(threadID, turnID string) error {
	_, err := s.rpc.Request("turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
	return err
}

// settledAttachment is an attachment whose resume already happened: creating a
// task is a successful resume in the same sense.
func settledAttachment() *taskAttachment {
	attachment := &taskAttachment{done: make(chan struct{})}
	close(attachment.done)
	return attachment
}

// readNativeSettings reads one native settings object. Native responses carry the
// effort as `reasoningEffort` while an update notification carries it as `effort`,
// so the caller names the key. A model or effort key counts only when it carries
// text, because a null wildcard falls back to the native thread; a present service
// tier key is always known, and null is its native default. A present key of any
// other type is read as the missing value, which is a malformed-payload difference
// only.
func readNativeSettings(value any, effortKey string) nativeSessionSettings {
	fields, _ := nativejson.Fields(value)
	var settings nativeSessionSettings
	if text, ok := fields["model"].(string); ok {
		settings.Model = &text
	}
	if text, ok := fields[effortKey].(string); ok {
		settings.Effort = &text
	}
	if raw, present := fields["serviceTier"]; present {
		var tier *string
		if text, ok := raw.(string); ok {
			tier = &text
		}
		settings.ServiceTier = protocol.Known(tier)
	}
	return settings
}

// readTaskUsage reads the token accounting of one usage notification. A missing
// field is unknown, which the runtime output reports as null.
func readTaskUsage(value any) taskUsage {
	fields, _ := nativejson.Fields(value)
	last, _ := nativejson.Fields(fields["last"])
	var usage taskUsage
	if number, ok := last["totalTokens"].(float64); ok {
		usage.TotalTokens = &number
	}
	if number, ok := fields["modelContextWindow"].(float64); ok {
		usage.ContextWindow = &number
	}
	return usage
}

// optionalNullableText keeps the wire difference between an absent optional field
// and an explicit null, which the runtime output preserves.
func optionalNullableText(fields map[string]any, key string) protocol.Optional[*string] {
	if _, present := fields[key]; !present {
		return protocol.Optional[*string]{}
	}
	if text, ok := fields[key].(string); ok {
		return protocol.Known(&text)
	}
	return protocol.Known[*string](nil)
}
