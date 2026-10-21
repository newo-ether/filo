package sessions

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/protocol"
)

// ConnectionBoundIPC sends only on the named, already connected IPC exchange.
// Implementations must not reconnect, substitute an exchange or replay the call.
// Zero generation means no verified live exchange is available.
type ConnectionBoundIPC interface {
	ConnectionGeneration() uint64
	RequestOnConnection(context.Context, uint64, string, int, any, string) (desktop.Message, error)
}

// NativeAccess is implemented by an explicit creation/recovery scope only when
// it can validate its exact native executor again at the final dispatch boundary.
// A durable creation record or a matching canonical identity alone is insufficient.
type NativeAccess interface {
	Access(context.Context, string) (protocol.SessionAccess, error)
	ValidateAccess(context.Context, string, string) error
}

type desktopAccess struct {
	binding, owner, nativeBinding string
	scope                         string
	entry                         *followed
	connection                    uint64
	capabilities                  protocol.AdapterCapabilities
}

type accessAdmissionKey struct{}

type accessAdmission struct {
	sessions *DesktopSessions
	id       string
	access   desktopAccess
}

// Access reports verified account/store identity and current owner capabilities.
// It never enters mutationDestination or allocates a writer.
func (sessions *DesktopSessions) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	access, err := sessions.currentAccess(ctx, id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	return publicAccess(id, access), nil
}

// CatalogAccess is cheap identity-only projection. It never discovers an owner,
// subscribes to a full native snapshot or consults creation/recovery admission.
func (sessions *DesktopSessions) CatalogAccess(ctx context.Context, id string) (protocol.SessionAccess, error) {
	scope, err := sessions.accessScope(ctx, id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	return publicAccess(id, desktopAccess{scope: scope,
		binding: sessions.sealAccess("catalog", scope, id), capabilities: sessions.readCapabilities()}), nil
}

func publicAccess(id string, access desktopAccess) protocol.SessionAccess {
	encoded, _ := json.Marshal([3]string{"codex", access.scope, id})
	canonical := sha256.Sum256(encoded)
	return protocol.SessionAccess{NativeScopeID: access.scope,
		CanonicalSessionRef: base64.RawURLEncoding.EncodeToString(canonical[:]),
		Binding:             access.binding, Capabilities: access.capabilities}
}

var accessScopePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func (sessions *DesktopSessions) accessScope(ctx context.Context, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if sessions.f.isStopped() || sessions.options.AccessScope == nil || id == "" || len(id) > 512 ||
		strings.ContainsAny(id, "/\\\x00\r\n") {
		return "", unavailable()
	}
	scope, err := sessions.options.AccessScope(ctx)
	if err != nil {
		return "", err
	}
	if !accessScopePattern.MatchString(scope) {
		return "", unavailable()
	}
	return scope, ctx.Err()
}

func (sessions *DesktopSessions) ValidateAccess(ctx context.Context, id, binding string) error {
	access, err := sessions.currentAccess(ctx, id)
	if err != nil {
		return err
	}
	if !sameBinding(binding, access.binding) {
		return unavailable()
	}
	return nil
}

func sameBinding(expected, actual string) bool {
	return expected != "" && hmac.Equal([]byte(expected), []byte(actual))
}

func (sessions *DesktopSessions) sealAccess(fields ...string) string {
	data, _ := json.Marshal(fields)
	mac := hmac.New(sha256.New, sessions.accessSecret[:])
	_, _ = mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func accessSecret() [32]byte {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		panic("Cannot initialize native session access identity")
	}
	return secret
}

func (sessions *DesktopSessions) readCapabilities() protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{
		History: sessions.options.History != nil, LazyMessages: sessions.options.History != nil,
		Models: sessions.options.Base != nil, HistoryMode: "bounded",
		ActiveSend: "unsupported", Settings: []string{},
	}
}

func (sessions *DesktopSessions) currentAccess(ctx context.Context, id string) (desktopAccess, error) {
	scope, err := sessions.accessScope(ctx, id)
	if err != nil {
		return desktopAccess{}, err
	}
	access, err := sessions.resolveAccess(ctx, id)
	if err != nil {
		return access, err
	}
	confirmed, err := sessions.accessScope(ctx, id)
	if err != nil || confirmed != scope || access.scope != "" && access.scope != scope {
		if err == nil {
			err = unavailable()
		}
		return desktopAccess{}, err
	}
	access.scope = scope
	access.binding = sessions.sealAccess("verified", scope, id, access.binding)
	return access, nil
}

func (sessions *DesktopSessions) resolveAccess(ctx context.Context, id string) (desktopAccess, error) {
	access := desktopAccess{capabilities: sessions.readCapabilities()}
	owner, err := sessions.f.owner(ctx, id)
	if err != nil {
		return access, err
	}
	access.owner = owner
	if owner == "" {
		return sessions.scopeAccess(ctx, id, access)
	}
	entry, release, err := sessions.readState(ctx, id, owner, false)
	if err != nil {
		return access, err
	}
	defer release()
	if entry == nil {
		access.binding = sessions.sealAccess("unconfirmed", id, owner)
		return access, nil
	}
	sessions.f.mu.Lock()
	valid := !sessions.f.stopped && sessions.f.entries[id] == entry && entry.err == nil && entry.state != nil
	state := entry.state
	generation, epoch := entry.generation, entry.accessEpoch
	connection := entry.connectionGeneration
	sessions.f.mu.Unlock()
	if !valid {
		return access, unavailable()
	}
	access.entry, access.connection = entry, connection
	access.capabilities.Live = true
	access.binding = sessions.sealAccess("desktop", id, owner, fmt.Sprint(generation), fmt.Sprint(epoch), fmt.Sprint(connection))
	bound, ok := sessions.options.IPC.(ConnectionBoundIPC)
	if !ok || connection == 0 || bound.ConnectionGeneration() != connection {
		return access, nil
	}
	status := codex.DesktopStatus(state)
	if status.Status == nil {
		return access, nil
	}
	idle := *status.Status == "idle" && status.ActiveTurnID == nil
	active := *status.Status == "active" && status.ActiveTurnID != nil && *status.ActiveTurnID != ""
	if idle || active {
		access.capabilities.IdleSend = idle
		if active {
			access.capabilities.ActiveSend, access.capabilities.Stop = "native-steer", true
		}
		if sessions.options.Base != nil {
			access.capabilities.Settings = []string{"model", "effort", "serviceTier"}
		}
		access.capabilities.Uploads = true
	}
	return access, nil
}

func (sessions *DesktopSessions) scopeAccess(ctx context.Context, id string, access desktopAccess) (desktopAccess, error) {
	access.binding = sessions.sealAccess("history", id)
	owned, err := sessions.created(ctx, id)
	if err != nil || !owned {
		return access, err
	}
	provider, ok := sessions.options.Created.(NativeAccess)
	if !ok || sessions.options.WithAccessBinding == nil {
		return access, nil
	}
	native, err := provider.Access(ctx, id)
	if err != nil {
		return access, err
	}
	if native.Binding == "" {
		return access, unavailable()
	}
	// The scope itself owns all executor-generation/turn evidence. Its control
	// methods must consume the translated binding rather than resolve a new owner.
	if err := provider.ValidateAccess(ctx, id, native.Binding); err != nil {
		return access, err
	}
	owner, err := sessions.f.owner(ctx, id)
	if err != nil || owner != "" {
		if err == nil {
			err = unavailable()
		}
		return access, err
	}
	if native.Capabilities.ActiveSend != "" && native.Capabilities.ActiveSend != "unsupported" &&
		native.Capabilities.ActiveSend != "native-steer" {
		return access, unavailable()
	}
	access.scope = native.NativeScopeID
	access.nativeBinding = native.Binding
	access.binding = sessions.sealAccess("scope", id, native.Binding)
	access.capabilities.Live = native.Capabilities.Live
	access.capabilities.IdleSend = native.Capabilities.IdleSend
	if native.Capabilities.ActiveSend != "" {
		access.capabilities.ActiveSend = native.Capabilities.ActiveSend
	}
	access.capabilities.Stop = native.Capabilities.Stop
	access.capabilities.Settings = append([]string{}, native.Capabilities.Settings...)
	access.capabilities.Uploads = native.Capabilities.Uploads
	return access, nil
}

func (sessions *DesktopSessions) admitAccess(ctx context.Context, id string) (context.Context, error) {
	if sessions.options.AccessBinding == nil {
		return ctx, nil
	}
	binding := sessions.options.AccessBinding(ctx)
	if binding == "" {
		return ctx, nil // Legacy unscoped behavior has its original owner checks.
	}
	if admission, ok := ctx.Value(accessAdmissionKey{}).(*accessAdmission); ok {
		if admission.sessions != sessions || admission.id != id || !sameBinding(binding, admission.access.binding) {
			return ctx, unavailable()
		}
		return ctx, nil
	}
	access, err := sessions.currentAccess(ctx, id)
	if err != nil {
		return ctx, err
	}
	if !sameBinding(binding, access.binding) {
		return ctx, unavailable()
	}
	return context.WithValue(ctx, accessAdmissionKey{}, &accessAdmission{sessions, id, access}), nil
}

func (sessions *DesktopSessions) admission(ctx context.Context, id string) (*accessAdmission, error) {
	value, ok := ctx.Value(accessAdmissionKey{}).(*accessAdmission)
	if !ok {
		return nil, nil
	}
	if value.sessions != sessions || value.id != id {
		return nil, unavailable()
	}
	return value, nil
}

func (sessions *DesktopSessions) scopedDestination(ctx context.Context, id string) (string, bool, error) {
	admission, err := sessions.admission(ctx, id)
	if err != nil || admission == nil {
		return "", admission != nil, err
	}
	access := admission.access
	if err := sessions.ValidateAccess(ctx, id, access.binding); err != nil {
		return "", true, err
	}
	if access.owner == "" && access.nativeBinding == "" {
		return "", true, unavailable()
	}
	return access.owner, true, nil
}

func (sessions *DesktopSessions) scopedMutationContext(ctx context.Context, id, operation string) (context.Context, error) {
	admission, err := sessions.admission(ctx, id)
	if err != nil || admission == nil {
		return ctx, err
	}
	access := admission.access
	caps := access.capabilities
	allowed := operation == "send" && (caps.IdleSend || caps.ActiveSend == "native-steer") ||
		operation == "stop" && caps.Stop || operation == "settings" && len(caps.Settings) > 0
	if !allowed {
		return ctx, unavailable()
	}
	if access.owner == "" {
		if err := sessions.ValidateAccess(ctx, id, access.binding); err != nil {
			return ctx, err
		}
		ctx = sessions.options.WithAccessBinding(ctx, access.nativeBinding)
	}
	return ctx, ctx.Err()
}

func (sessions *DesktopSessions) scopedSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	admission, err := sessions.admission(ctx, id)
	if err != nil || admission == nil {
		return err
	}
	allowed := map[string]bool{}
	for _, field := range admission.access.capabilities.Settings {
		allowed[field] = true
	}
	if settings.Model.Known && !allowed["model"] || settings.Effort.Known && !allowed["effort"] ||
		settings.ServiceTier.Known && !allowed["serviceTier"] {
		return unavailable()
	}
	return nil
}

func (sessions *DesktopSessions) requestOperation(ctx context.Context, id, owner, method string, version int, params map[string]any) (desktop.Message, error) {
	admission, err := sessions.admission(ctx, id)
	if err != nil {
		return nil, err
	}
	if admission == nil {
		return sessions.options.IPC.Request(ctx, method, version, params, owner)
	}
	access := admission.access
	bound, ok := sessions.options.IPC.(ConnectionBoundIPC)
	if !ok || access.owner != owner || access.entry == nil || access.connection == 0 || bound.ConnectionGeneration() != access.connection {
		return nil, unavailable()
	}
	scope, err := sessions.accessScope(ctx, id)
	if err != nil || scope != access.scope {
		if err == nil {
			err = unavailable()
		}
		return nil, err
	}
	sessions.f.mu.Lock()
	entry := access.entry
	valid := !sessions.f.stopped && sessions.f.entries[id] == entry && entry.err == nil &&
		entry.owner == owner && sameBinding(access.binding, sessions.sealAccess("verified", scope, id,
		sessions.sealAccess("desktop", id, owner, fmt.Sprint(entry.generation),
			fmt.Sprint(entry.accessEpoch), fmt.Sprint(entry.connectionGeneration))))
	sessions.f.mu.Unlock()
	if !valid {
		return nil, unavailable()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bound.RequestOnConnection(ctx, access.connection, method, version, params, owner)
}

func desktopTurnIdentity(state codex.DesktopState) string {
	status := codex.DesktopStatus(state)
	identity, _ := json.Marshal([]any{status.Status, status.ActiveTurnID, status.CompletedTurnID})
	return string(identity)
}
