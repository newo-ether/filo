// Package codexcli provides bounded observation of one verified Codex CLI store.
// It never loads a thread, starts a daemon, or admits native execution.
package codexcli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

// History is the existing selected-user read-only history projection. The
// supplier must resolve its home inside the same verified account/store scope.
type History interface {
	List(context.Context, string) (protocol.SessionPage, error)
	Read(context.Context, string, string, bool, []string) (protocol.ConversationPage, error)
}

type Options struct {
	SocketPath    string
	UserID        string
	NativeScopeID string
	History       History
	VerifyScope   func(context.Context) error
	// Scope optionally resolves the current verified store/account identity.
	// When set it is authoritative; failed verification never uses an old scope.
	Scope        func(context.Context) (string, error)
	VerifyDaemon func(context.Context, codex.LocalDaemonIdentity) error
	// SessionIdentity lets the common identity owner supply its canonical ref
	// and binding. The adapter always replaces its capabilities with read-only.
	SessionIdentity func(context.Context, string) (protocol.SessionAccess, error)
	Timeout         time.Duration
}

// Status separates readable verified history from the optional daemon metadata
// connection. Readable history alone never establishes a live native connection.
type Status struct {
	State        string
	Reason       string
	DaemonState  string
	Capabilities protocol.AdapterCapabilities
}

type Adapter struct {
	options       Options
	mu            sync.Mutex
	closed        bool
	daemonState   string
	connections   map[*codex.CodexHost]struct{}
	metadataReads int
}

var nativeIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var scopePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
var ErrUnsupported = errors.New("Operation is unsupported by this source/session")
var ErrUnavailable = errors.New("Verified Codex CLI store is unavailable")

// New does not read history, contact a daemon, or inspect account credentials.
func New(options Options) (*Adapter, error) {
	if options.History == nil || options.Timeout < 0 ||
		(options.Scope == nil && (options.VerifyScope == nil || !scopePattern.MatchString(options.NativeScopeID))) ||
		(options.SocketPath != "" && (options.UserID == "" || options.VerifyDaemon == nil)) {
		return nil, errors.New("Invalid Codex CLI adapter configuration")
	}
	return &Adapter{options: options, daemonState: "disconnected", connections: make(map[*codex.CodexHost]struct{})}, nil
}

// MetadataCapabilities describes implemented read operations for a configured
// daemon endpoint. It does not prove reachability or grant native execution.
func MetadataCapabilities(configuredEndpoint bool) protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{
		History: true, LazyMessages: true, ActiveSend: "unsupported", HistoryMode: "bounded", Settings: []string{},
		Models: configuredEndpoint, Usage: configuredEndpoint,
	}
}

// Capabilities reads only configuration. Status and every metadata request verify
// the actual peer and native store independently before reporting native results.
func (a *Adapter) Capabilities() protocol.AdapterCapabilities {
	return MetadataCapabilities(a.options.SocketPath != "")
}

func scopeFailure(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrUnavailable
}

func (a *Adapter) scope(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return "", ErrUnavailable
	}
	if a.options.VerifyScope != nil {
		if err := a.options.VerifyScope(ctx); err != nil {
			return "", scopeFailure(ctx, err)
		}
	}
	scope := a.options.NativeScopeID
	if a.options.Scope != nil {
		var err error
		scope, err = a.options.Scope(ctx)
		if err != nil {
			return "", scopeFailure(ctx, err)
		}
	}
	if !scopePattern.MatchString(scope) {
		return "", scopeFailure(ctx, nil)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return scope, nil
}

func (a *Adapter) Status(ctx context.Context) Status {
	if _, err := a.scope(ctx); err != nil {
		return Status{State: "unavailable", Reason: "native_scope_unavailable", DaemonState: "disconnected",
			Capabilities: protocol.AdapterCapabilities{ActiveSend: "unsupported", HistoryMode: "bounded", Settings: []string{}}}
	}
	if a.options.SocketPath == "" {
		return Status{State: "disconnected", Reason: "native_connection_unavailable", DaemonState: "disconnected", Capabilities: a.Capabilities()}
	}
	ctx, cancel := context.WithTimeout(ctx, a.readTimeout())
	defer cancel()
	_, release, err := a.metadata(ctx)
	if err != nil {
		return Status{State: "disconnected", Reason: "native_connection_unavailable", DaemonState: "unavailable", Capabilities: a.Capabilities()}
	}
	release()
	return Status{State: "connected", DaemonState: "connected", Capabilities: a.Capabilities()}
}

// Access uses a verified stable native scope, never a socket path or process ID.
func (a *Adapter) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	if !nativeIDPattern.MatchString(id) {
		return protocol.SessionAccess{}, errors.New("Invalid native Codex session identity")
	}
	scope, err := a.scope(ctx)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	access, err := a.identity(ctx, scope, id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	if err := a.sameScope(ctx, scope); err != nil {
		return protocol.SessionAccess{}, err
	}
	return access, nil
}

// identity projects one row inside the caller's captured read scope. The caller
// must revalidate that scope before publishing the bounded page.
func (a *Adapter) identity(ctx context.Context, scope, id string) (protocol.SessionAccess, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionAccess{}, err
	}
	var access protocol.SessionAccess
	var err error
	if a.options.SessionIdentity != nil {
		access, err = a.options.SessionIdentity(ctx, id)
		if err != nil || access.NativeScopeID != scope || !scopePattern.MatchString(access.CanonicalSessionRef) || !scopePattern.MatchString(access.Binding) {
			return protocol.SessionAccess{}, scopeFailure(ctx, err)
		}
	} else {
		encoded, _ := json.Marshal([3]string{"codex", scope, id})
		canonical := sha256.Sum256(encoded)
		binding := sha256.Sum256(append([]byte("codex-cli-read-only:"), encoded...))
		access = protocol.SessionAccess{NativeScopeID: scope, CanonicalSessionRef: base64.RawURLEncoding.EncodeToString(canonical[:]),
			Binding: base64.RawURLEncoding.EncodeToString(binding[:])}
	}
	access.Capabilities = a.Capabilities()
	return access, ctx.Err()
}

// Close releases only connections opened by this adapter. It never closes the
// supplied history owner or signals a daemon or another native client.
func (a *Adapter) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	connections := a.connections
	a.connections = make(map[*codex.CodexHost]struct{})
	a.mu.Unlock()
	for host := range connections {
		host.Close()
	}
}
