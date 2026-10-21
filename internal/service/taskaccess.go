package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// Access observes only an already admitted native connection. In particular it
// never calls acquire, Follow, Open or thread/resume to manufacture authority.
func (s *TaskSessions) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionAccess{}, err
	}
	s.mu.Lock()
	entry, draining := s.entries[id], s.draining
	s.mu.Unlock()
	if draining {
		return protocol.SessionAccess{}, shuttingDown()
	}
	if entry == nil {
		return protocol.SessionAccess{Binding: s.taskBinding(id, "dormant"),
			Capabilities: taskReadCapabilities()}, nil
	}
	return s.entryAccess(ctx, id, entry)
}

func (s *TaskSessions) ValidateAccess(ctx context.Context, id, binding string) error {
	access, err := s.Access(ctx, id)
	if err != nil {
		return err
	}
	if binding == "" || !hmac.Equal([]byte(binding), []byte(access.Binding)) {
		return taskAccessChanged()
	}
	return nil
}

func taskReadCapabilities() protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{History: true, LazyMessages: true,
		HistoryMode: "bounded", ActiveSend: "unsupported", Settings: []string{}}
}

func (s *TaskSessions) taskBinding(fields ...string) string {
	data, _ := json.Marshal(fields)
	mac := hmac.New(sha256.New, []byte(s.accessSecret))
	_, _ = mac.Write(data)
	return "task1_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *TaskSessions) entryAccess(ctx context.Context, id string, entry *taskSessionsEntry) (protocol.SessionAccess, error) {
	return s.taskEntryAccess(ctx, id, entry, "", nil)
}

func (s *TaskSessions) taskEntryAccess(ctx context.Context, id string, entry *taskSessionsEntry, method string, params any) (protocol.SessionAccess, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionAccess{}, err
	}
	s.mu.Lock()
	valid := !s.draining && s.entries[id] == entry && entry.executionOwner != nil &&
		entry.native.rpc == entry.executionOwner && entry.connection.Transport() == entry.executionOwner
	epoch := entry.accessEpoch
	s.mu.Unlock()
	if !valid {
		return protocol.SessionAccess{}, taskAccessChanged()
	}
	snapshot, err := entry.sessions.Snapshot(id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	s.mu.Lock()
	valid = !s.draining && s.entries[id] == entry && entry.accessEpoch == epoch &&
		entry.native.rpc == entry.executionOwner && entry.connection.Transport() == entry.executionOwner
	s.mu.Unlock()
	if !valid {
		return protocol.SessionAccess{}, taskAccessChanged()
	}
	runtime := snapshot.Page.Runtime
	var status string
	var activeTurn, completedTurn *string
	var completedKnown bool
	if runtime != nil {
		status, activeTurn = runtime.Status, runtime.ActiveTurnID
		completedKnown, completedTurn = runtime.CompletedTurnID.Known, runtime.CompletedTurnID.Value
	}
	if method == "turn/steer" || method == "turn/interrupt" {
		fields, _ := nativejson.Fields(params)
		turnField := "expectedTurnId"
		if method == "turn/interrupt" {
			turnField = "turnId"
		}
		if activeTurn == nil || nativejson.Text(fields[turnField]) != *activeTurn {
			return protocol.SessionAccess{}, activeTurnChanged()
		}
	}
	identity, _ := json.Marshal([]any{epoch, snapshot.TurnID, status, activeTurn, completedKnown, completedTurn})
	caps := taskReadCapabilities()
	caps.Live = true
	if runtime != nil {
		idle := runtime.Status == "idle" && runtime.ActiveTurnID == nil
		active := runtime.Status == "active" && runtime.ActiveTurnID != nil && *runtime.ActiveTurnID != ""
		caps.IdleSend = idle
		if active {
			caps.ActiveSend, caps.Stop = "native-steer", true
		}
		if idle || active {
			caps.Settings = []string{"model", "effort", "serviceTier"}
			caps.Uploads = true
		}
	}
	return protocol.SessionAccess{Binding: s.taskBinding(id, entry.accessID, string(identity)), Capabilities: caps}, ctx.Err()
}

func taskAccessChanged() error {
	return CodexRpcError("Native task access binding changed; refresh before continuing", -32600)
}

// nativeTaskSessionsConnection retains the actual admitted RPC. It never
// resolves or reconnects a native owner. Mutations carry their request context
// all the way through NativeTaskSession's internal settings/turn calls.
type nativeTaskSessionsConnection struct {
	rpc      *codex.Rpc
	mu       sync.Mutex
	ctx      context.Context
	validate func(context.Context, string, string, any) error
}

func (c *nativeTaskSessionsConnection) Request(method string, params any) (any, error) {
	return c.RequestTimeout(method, params, 30*time.Second)
}

func (c *nativeTaskSessionsConnection) RequestTimeout(method string, params any, timeout time.Duration) (any, error) {
	c.mu.Lock()
	ctx := c.ctx
	c.mu.Unlock()
	if ctx != nil && protocol.AccessBinding(ctx) != "" {
		switch method {
		case "thread/resume", "turn/start", "turn/steer", "turn/interrupt", "thread/settings/update":
			if c.validate == nil {
				return nil, taskAccessChanged()
			}
			if err := c.validate(ctx, protocol.AccessBinding(ctx), method, params); err != nil {
				return nil, err
			}
		}
	}
	if ctx != nil {
		if err := protocol.ValidateScope(ctx); err != nil {
			return nil, err
		}
	}
	return c.rpc.RequestTimeout(method, params, timeout)
}

func (s *TaskSessions) bindTaskMutation(ctx context.Context, id string, entry *taskSessionsEntry) (func(), error) {
	entry.operation.Lock()
	if err := protocol.ValidateScope(ctx); err != nil {
		entry.operation.Unlock()
		return nil, err
	}
	release := func() {
		entry.native.mu.Lock()
		entry.native.ctx = nil
		entry.native.mu.Unlock()
		entry.operation.Unlock()
	}
	if binding := protocol.AccessBinding(ctx); binding != "" {
		access, err := s.entryAccess(ctx, id, entry)
		if err != nil || !hmac.Equal([]byte(binding), []byte(access.Binding)) {
			entry.operation.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, taskAccessChanged()
		}
	}
	entry.native.mu.Lock()
	entry.native.ctx = ctx
	entry.native.mu.Unlock()
	return release, nil
}

func (s *TaskSessions) taskNative(connection TaskSessionsConnection) *nativeTaskSessionsConnection {
	return &nativeTaskSessionsConnection{rpc: connection.Transport()}
}

func (s *TaskSessions) registerTaskAccess(id string, entry *taskSessionsEntry) {
	entry.native.validate = func(ctx context.Context, binding, method string, params any) error {
		fields, _ := nativejson.Fields(params)
		if nativejson.Text(fields["threadId"]) != id {
			return taskAccessChanged()
		}
		access, err := s.taskEntryAccess(ctx, id, entry, method, params)
		if err != nil {
			return err
		}
		if !hmac.Equal([]byte(binding), []byte(access.Binding)) {
			return taskAccessChanged()
		}
		caps := access.Capabilities
		allowed := method == "turn/start" && caps.IdleSend ||
			method == "turn/steer" && caps.ActiveSend == "native-steer" ||
			method == "turn/interrupt" && caps.Stop ||
			method == "thread/settings/update" && len(caps.Settings) > 0
		if !allowed {
			return taskAccessChanged()
		}
		return ctx.Err()
	}
}

func (s *TaskSessions) observeTaskAccess(id string, entry *taskSessionsEntry, method string, params any) {
	switch method {
	case "turn/started", "turn/completed", "thread/closed":
		fields, _ := nativejson.Fields(params)
		if nativejson.Text(fields["threadId"]) != id {
			return
		}
		s.mu.Lock()
		entry.accessEpoch++
		s.mu.Unlock()
	}
}

// Access on the gateway consults the helper's dedicated read-only access path,
// rather than a cached page or a history read that could acquire a follower.
func (c *CreatedTaskClient) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	if err := c.admit(id); err != nil {
		return protocol.SessionAccess{}, err
	}
	raw, err := c.call(ctx, "/v1/sessions/"+id+"/access", nil)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	var access protocol.SessionAccess
	if err := json.Unmarshal(raw, &access); err != nil || access.Binding == "" {
		return protocol.SessionAccess{}, errors.New("Native task access is unconfirmed")
	}
	return access, nil
}

func (c *CreatedTaskClient) ValidateAccess(ctx context.Context, id, binding string) error {
	access, err := c.Access(ctx, id)
	if err != nil {
		return err
	}
	if binding == "" || !hmac.Equal([]byte(binding), []byte(access.Binding)) {
		return taskAccessChanged()
	}
	return nil
}

func (c *CreatedTaskClient) callMutation(ctx context.Context, id, action string, input any) (json.RawMessage, error) {
	binding := protocol.AccessBinding(ctx)
	if binding != "" {
		if err := c.ValidateAccess(ctx, id, binding); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		var body map[string]any
		if err := json.Unmarshal(encoded, &body); err != nil {
			return nil, err
		}
		if body == nil {
			body = map[string]any{}
		}
		body["accessBinding"] = binding
		if nativeScope := protocol.NativeScopeID(ctx); nativeScope != "" {
			body["nativeScopeId"] = nativeScope
		}
		input = body
	}
	return c.call(ctx, "/v1/sessions/"+id+"/"+action, input)
}

func (scope createdScope) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	return scope.tasks.Access(ctx, id)
}

func (scope createdScope) ValidateAccess(ctx context.Context, id, binding string) error {
	return scope.tasks.ValidateAccess(ctx, id, binding)
}
