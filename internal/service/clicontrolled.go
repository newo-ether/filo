package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
)

// CLIControlledOptions supplies the existing verified reader and native history.
// Directory is the private worker root; CLI provenance is isolated beneath it.
// Construction performs no native launch, handshake or model request.
type CLIControlledOptions struct {
	Directory  string
	Workspace  string
	Reader     *codexcli.Adapter
	Metadata   CLIReadMetadata
	History    TaskSessionsHistory
	Scope      func(context.Context) (string, error)
	OnChange   func(string)
	Launch     ExecutorLauncher
	Readiness  time.Duration
	NativeHome func(context.Context) (string, error)
	// Daemon connects only to an already-running verified native daemon.
	Daemon func(context.Context) (*codex.CodexHost, error)
}

// CLIControlledSessions controls admitted executors and explicitly shared native
// terminals. Historical identities alone never grant mutation authority.
type CLIControlledSessions struct {
	*TaskSessions
	reader        *codexcli.Adapter
	metadata      CLIReadMetadata
	factory       *TaskExecutorFactory
	scope         func(context.Context) (string, error)
	shared        *TaskSessions
	sharedFactory *cliSharedFactory
	catalogMu     sync.Mutex
}

func NewCLIControlledSessions(options CLIControlledOptions) (*CLIControlledSessions, error) {
	if options.Reader == nil || options.History == nil || options.Scope == nil {
		return nil, errors.New("CLI execution requires verified native reader and scope")
	}
	factory, err := NewTaskExecutorFactory(TaskExecutorFactoryOptions{
		Directory: filepath.Join(options.Directory, "codex-cli-tasks"), Source: codexCLISource,
		Workspace: options.Workspace, Launch: options.Launch, Readiness: options.Readiness,
	})
	if err != nil {
		return nil, err
	}
	metadata := cliControlledMetadata{reader: options.Reader, metadata: options.Metadata}
	tasks := NewTaskSessions(metadata, options.History, factory)
	tasks.SetChangedHandler(options.OnChange)
	surface := &CLIControlledSessions{TaskSessions: tasks, reader: options.Reader, metadata: options.Metadata,
		factory: factory, scope: options.Scope}
	if options.NativeHome != nil && options.Daemon != nil {
		surface.sharedFactory = &cliSharedFactory{home: options.NativeHome, daemon: options.Daemon, tasks: map[string]cliSharedRecord{}}
		surface.shared = NewTaskSessionsWith(metadata, options.History, surface.sharedFactory)
		surface.shared.SetChangedHandler(options.OnChange)
	}
	return surface, nil
}

func (s *CLIControlledSessions) Capabilities() protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{Create: true, History: true, Live: true, IdleSend: true,
		Stop: true, ActiveSend: "native-steer", Settings: []string{"model", "effort", "serviceTier"},
		Rename: true, Archive: true, Uploads: true,
		Models: s.metadata != nil || s.reader.Capabilities().Models,
		Usage:  s.metadata != nil || s.reader.Capabilities().Usage, LazyMessages: true, HistoryMode: "bounded"}
}

func (s *CLIControlledSessions) fenced(ctx context.Context) (context.Context, error) {
	before, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	return protocol.WithScopeValidation(ctx, func(call context.Context) error {
		if err := protocol.ValidateScope(ctx); err != nil {
			return err
		}
		after, err := s.scope(call)
		if err != nil {
			return err
		}
		if before != after {
			return codexcli.ErrUnavailable
		}
		return nil
	}), nil
}

func (s *CLIControlledSessions) Create(ctx context.Context, text, clientID string, settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	ctx, err := s.fenced(ctx)
	if err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	if text == "" {
		return protocol.Session{}, protocol.SendReceipt{}, errors.New("First message is required")
	}
	return s.TaskSessions.Create(ctx, text, clientID, settings)
}

func (s *CLIControlledSessions) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	if s.isShared(id) {
		return s.sharedAccess(ctx, id)
	}
	scope, err := s.scope(ctx)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	owned, err := s.factory.Owns(id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	if !owned {
		return s.reader.Access(ctx, id)
	}
	access, err := s.TaskSessions.Access(ctx, id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	if !access.Capabilities.Live {
		page, err := s.TaskSessions.history.Read(ctx, id, "", false, nil)
		if err != nil {
			return protocol.SessionAccess{}, err
		}
		if sessions.RecoverableRuntime(page.Runtime) {
			access.Capabilities.IdleSend = true
			access.Capabilities.Settings = []string{"model", "effort", "serviceTier"}
			completed := ""
			if page.Runtime.CompletedTurnID.Value != nil {
				completed = *page.Runtime.CompletedTurnID.Value
			}
			access.Binding = s.TaskSessions.taskBinding(id, "cli-idle", completed)
		}
	}
	// Owned tasks rename/archive on their own native connection; an idle task is
	// admitted only through the same exact-idle recovery used by Send.
	if access.Capabilities.Live || access.Capabilities.IdleSend {
		access.Capabilities.Rename, access.Capabilities.Archive = true, true
	}
	after, err := s.scope(ctx)
	if err != nil || after != scope {
		return protocol.SessionAccess{}, codexcli.ErrUnavailable
	}
	identity := codexReadIdentity(scope, id)
	access.NativeScopeID, access.CanonicalSessionRef = scope, identity.CanonicalSessionRef
	access.Binding = s.TaskSessions.taskBinding(id, "cli-scope", scope, access.Binding)
	return access, protocol.ValidateScope(ctx)
}

func (s *CLIControlledSessions) CatalogAccess(ctx context.Context, id string) (protocol.SessionAccess, error) {
	return s.Access(ctx, id)
}

func (s *CLIControlledSessions) ValidateAccess(ctx context.Context, id, binding string) error {
	access, err := s.Access(ctx, id)
	if err != nil {
		return err
	}
	if binding == "" || binding != access.Binding {
		return taskAccessChanged()
	}
	return nil
}

func (s *CLIControlledSessions) mutationContext(ctx context.Context, id string, recoverIdle bool) (context.Context, error) {
	ctx, err := s.fenced(ctx)
	if err != nil {
		return nil, err
	}
	owned, err := s.factory.Owns(id)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, codexcli.ErrUnsupported
	}
	access, err := s.Access(ctx, id)
	if err != nil {
		return nil, err
	}
	binding := protocol.AccessBinding(ctx)
	if binding != "" && binding != access.Binding {
		return nil, taskAccessChanged()
	}
	if !access.Capabilities.Live {
		if !recoverIdle || !access.Capabilities.IdleSend {
			return nil, taskAccessChanged()
		}
		// TaskSessions rechecks persisted exact-task idleness before allocating;
		// native resume still must acquire its writer lock without takeover.
		return sessions.WithIdleRecovery(protocol.WithAccessBinding(ctx, ""), id), nil
	}
	inner, err := s.TaskSessions.Access(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.TaskSessions.taskBinding(id, "cli-scope", access.NativeScopeID, inner.Binding) != access.Binding {
		return nil, taskAccessChanged()
	}
	if binding != "" {
		ctx = protocol.WithAccessBinding(ctx, inner.Binding)
	}
	return ctx, nil
}

func (s *CLIControlledSessions) Send(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	if s.isShared(id) {
		ctx, err := s.sharedContext(ctx, id)
		if err != nil {
			return protocol.SendReceipt{}, err
		}
		return s.shared.Send(ctx, id, text, clientID)
	}
	ctx, err := s.mutationContext(ctx, id, true)
	if err != nil {
		return protocol.SendReceipt{}, err
	}
	return s.TaskSessions.Send(ctx, id, text, clientID)
}

func (s *CLIControlledSessions) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	if s.isShared(id) {
		ctx, err := s.sharedContext(ctx, id)
		if err != nil {
			return err
		}
		return s.shared.UpdateSettings(ctx, id, settings)
	}
	ctx, err := s.mutationContext(ctx, id, true)
	if err != nil {
		return err
	}
	return s.TaskSessions.UpdateSettings(ctx, id, settings)
}

func (s *CLIControlledSessions) SetModel(ctx context.Context, id, model string) error {
	return s.UpdateSettings(ctx, id, protocol.SessionSettings{Model: protocol.Known(model)})
}

func (s *CLIControlledSessions) Stop(ctx context.Context, id, turnID string) error {
	if s.isShared(id) {
		ctx, err := s.sharedContext(ctx, id)
		if err != nil {
			return err
		}
		return s.shared.Stop(ctx, id, turnID)
	}
	ctx, err := s.mutationContext(ctx, id, false)
	if err != nil {
		return err
	}
	return s.TaskSessions.Stop(ctx, id, turnID)
}

func (s *CLIControlledSessions) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	ctx, err := s.mutationContext(ctx, id, true)
	if err != nil {
		return protocol.Session{}, err
	}
	return s.TaskSessions.RenameOwned(ctx, id, name)
}

func (s *CLIControlledSessions) Archive(ctx context.Context, id string) (string, error) {
	ctx, err := s.mutationContext(ctx, id, true)
	if err != nil {
		return "", err
	}
	return s.TaskSessions.ArchiveOwned(ctx, id)
}

func (s *CLIControlledSessions) Close() {
	if s.metadata != nil {
		s.metadata.Close()
	}
	if s.shared != nil {
		s.shared.Close()
	}
	s.TaskSessions.Close()
}

var _ SessionAPI = (*CLIControlledSessions)(nil)

// liveOwnedIDs inspects only Filo's bounded executor registry. Durable creation
// records and native history are not catalog evidence; Access must still prove
// the exact existing owner after a read-only Follow.
func (s *CLIControlledSessions) liveOwnedIDs(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.entries))
	seen := map[string]bool{}
	for id := range s.entries {
		ids = append(ids, id)
		seen[id] = true
	}
	s.mu.Unlock()
	directory, err := os.Open(filepath.Join(s.factory.directory, "executors"))
	if errors.Is(err, os.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(129)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 128 {
		return nil, errors.New("CLI executor registry capacity exceeded")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !taskIDPattern.MatchString(entry.Name()) {
			continue
		}
		value, err := ReadExecutorFile(filepath.Join(s.factory.directory, "executors", entry.Name()), executorStateName)
		if err != nil {
			continue
		}
		state, err := ParseExecutorState(value)
		if err != nil || state.Phase != "ready" || state.TaskID == nil || seen[*state.TaskID] {
			continue
		}
		ids = append(ids, *state.TaskID)
		seen[*state.TaskID] = true
	}
	return ids, nil
}
