package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/uploads"
)

// Adapter registers one immutable, explicitly configured native access path.
// Scope is sampled for every request and checked again around native operations.
type Adapter struct {
	ID, Family, Origin, NativeScopeID string
	Scope                             func(context.Context) (string, error)
	Capabilities                      protocol.AdapterCapabilities
	// CapabilitySource may narrow the configured maximum using fresh native evidence.
	CapabilitySource func(context.Context) (protocol.AdapterCapabilities, error)
	Sessions         SessionAPI
	Status           func() AdapterStatus
	// StatusSource receives the discovery request context when status needs I/O.
	StatusSource func(context.Context) AdapterStatus
	Usage        func(context.Context) (protocol.AccountUsage, error)
	Uploads      *uploads.Store
}

type AdapterStatus struct {
	State string
	Error *string
}

type SessionAccessProvider interface {
	Access(context.Context, string) (protocol.SessionAccess, error)
	ValidateAccess(context.Context, string, string) error
}

// AccessBinding remains a compatibility accessor for native implementations.
func AccessBinding(ctx context.Context) string { return protocol.AccessBinding(ctx) }

type adapterSurface struct {
	Adapter
	root *Server
}

type adapterView struct {
	*adapterSurface
	scope        string
	Capabilities protocol.AdapterCapabilities
	server       *Server
}

var adapterIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var opaquePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
var statusCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func normalizedCaps(c protocol.AdapterCapabilities) (protocol.AdapterCapabilities, error) {
	if c.ActiveSend == "" {
		c.ActiveSend = "unsupported"
	}
	if c.HistoryMode == "" {
		c.HistoryMode = "bounded"
	}
	if c.Settings == nil {
		c.Settings = []string{}
	}
	if c.ActiveSend != "unsupported" && c.ActiveSend != "native-steer" ||
		c.HistoryMode != "complete" && c.HistoryMode != "bounded" || len(c.Settings) > 3 {
		return c, errors.New("Invalid adapter capabilities")
	}
	seen := map[string]bool{}
	for _, field := range c.Settings {
		if seen[field] || field != "model" && field != "effort" && field != "serviceTier" {
			return c, errors.New("Invalid adapter settings capabilities")
		}
		seen[field] = true
	}
	return c, nil
}

func readOnlyCaps(c protocol.AdapterCapabilities) protocol.AdapterCapabilities {
	c.Create, c.IdleSend, c.Stop, c.Rename, c.Archive, c.Uploads = false, false, false, false, false, false
	c.ActiveSend, c.Settings = "unsupported", []string{}
	return c
}

func (s *Server) configureAdapters(configured []Adapter) error {
	if configured == nil {
		return nil
	}
	if len(configured) == 0 || len(configured) > protocol.MaxAdapters {
		return errors.New("Expected one to four configured adapters")
	}
	s.adapters = make(map[string]*adapterSurface, len(configured))
	for _, a := range configured {
		validID := a.ID == codexDesktopSource || a.ID == codexCLISource || a.ID == claudeCodeSource || a.ID == claudeDesktopSource
		if !validID || s.adapters[a.ID] != nil || a.Family != "codex" && a.Family != "claude" ||
			a.Origin != "desktop" && a.Origin != "cli" || a.Scope == nil && !opaquePattern.MatchString(a.NativeScopeID) {
			return errors.New("Invalid configured adapter identity")
		}
		caps, err := normalizedCaps(a.Capabilities)
		if err != nil {
			return err
		}
		caps.Settings = append([]string{}, caps.Settings...)
		if a.Sessions == nil {
			caps = readOnlyCaps(caps)
			caps.History, caps.Live, caps.Models, caps.Usage, caps.LazyMessages = false, false, false, false, false
		}
		if _, ok := a.Sessions.(SessionAccessProvider); !ok {
			caps = readOnlyCaps(caps)
		}
		if a.Uploads != nil {
			if s.uploads == nil {
				s.uploads = a.Uploads
			}
			if s.uploads != a.Uploads {
				return errors.New("Adapters require one shared upload budget")
			}
		}
		if a.Usage == nil {
			caps.Usage = false
		}
		// Workers report native attachment permission; only the public gateway
		// owns the upload store. Upload routes still require a local store.
		if a.Uploads == nil && s.layer != "worker" {
			caps.Uploads = false
		}
		a.Capabilities = caps
		s.adapters[a.ID] = &adapterSurface{Adapter: a, root: s}
		s.adapterOrder = append(s.adapterOrder, a.ID)
	}
	return nil
}

func (a *adapterSurface) currentScope(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	scope := a.NativeScopeID
	if a.Scope != nil {
		var err error
		scope, err = a.Scope(ctx)
		if err != nil {
			return "", err
		}
	}
	if !opaquePattern.MatchString(scope) {
		return "", errors.New("Native source identity is unavailable")
	}
	return scope, nil
}

func (a *adapterSurface) descriptor(ctx context.Context) protocol.AdapterDescriptor {
	status := AdapterStatus{State: "unavailable"}
	if a.StatusSource != nil {
		status = a.StatusSource(ctx)
	} else if a.Status != nil {
		status = a.Status()
	}
	switch status.State {
	case "connected", "connecting", "disconnected", "unavailable":
	default:
		status.State = "unavailable"
	}
	if status.Error != nil && !statusCodePattern.MatchString(*status.Error) {
		status.Error = nil
	}
	scope, err := a.currentScope(ctx)
	if err != nil {
		status.State = "unavailable"
		code := "native_scope_unavailable"
		status.Error = &code
		// Discovery keeps a configured unavailable chip without retaining old authority.
		digest := hmac.New(sha256.New, []byte(a.root.expected))
		digest.Write([]byte("unavailable:" + a.ID))
		scope = base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
	}
	caps, capabilityErr := a.currentCapabilities(ctx)
	if capabilityErr != nil {
		status.State = "unavailable"
		code := "native_capabilities_unavailable"
		status.Error = &code
	}
	return protocol.AdapterDescriptor{ID: a.ID, Family: a.Family, Origin: a.Origin,
		State: status.State, Error: status.Error, NativeScopeID: scope, Capabilities: caps}
}

func (a *adapterSurface) view(ctx context.Context) (*adapterView, error) {
	scope, err := a.currentScope(ctx)
	if err != nil {
		return nil, err
	}
	caps, err := a.currentCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	v := &adapterView{adapterSurface: a, scope: scope, Capabilities: caps}
	root := a.root
	v.server = &Server{usage: a.Usage, uploads: a.Uploads, expected: root.expected, events: root.events,
		payloads: root.payloads, layer: "adapter-" + a.ID, createLayer: root.createLayer, hostname: root.hostname,
		createLog: root.createLog, imageOwner: root, scoped: v}
	v.server.sessions = &scopedSessions{SessionAPI: a.Sessions, view: v}
	return v, nil
}

func (v *adapterView) check(ctx context.Context) error {
	scope, err := v.currentScope(ctx)
	if err != nil {
		return err
	}
	if scope != v.scope {
		return CodexRpcError("Native source identity changed; refresh sources", -32600)
	}
	return nil
}

const scopedSessionPrefix = "filo.adapter."

func (v *adapterView) key(id string) string {
	data, _ := json.Marshal([3]string{v.ID, v.scope, id})
	return scopedSessionPrefix + base64.RawURLEncoding.EncodeToString(data)
}

func (v *adapterView) nativeID(key string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, scopedSessionPrefix))
	var fields [3]string
	if err != nil || !strings.HasPrefix(key, scopedSessionPrefix) || json.Unmarshal(data, &fields) != nil ||
		fields[0] != v.ID || fields[1] != v.scope || !validSessionRef(fields[2]) {
		return "", errors.New("Invalid scoped session reference")
	}
	return fields[2], nil
}

func validSessionRef(id string) bool {
	return len(id) > 0 && len(id) <= 512 && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00\r\n")
}

func (v *adapterView) seal(kind, session, value string) string {
	data, _ := json.Marshal([5]string{kind, v.ID, v.scope, session, value})
	mac := hmac.New(sha256.New, []byte(v.root.expected))
	mac.Write(data)
	return "filo.scope." + base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (v *adapterView) unseal(kind, session, token string) (string, error) {
	if len(token) > 24576 {
		return "", errors.New("Invalid scoped token")
	}
	parts := strings.Split(strings.TrimPrefix(token, "filo.scope."), ".")
	if !strings.HasPrefix(token, "filo.scope.") || len(parts) != 2 {
		return "", errors.New("Invalid scoped token")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("Invalid scoped token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, []byte(v.root.expected))
	mac.Write(data)
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return "", errors.New("Invalid scoped token")
	}
	var fields [5]string
	if json.Unmarshal(data, &fields) != nil || fields[0] != kind || fields[1] != v.ID || fields[2] != v.scope || fields[3] != session {
		return "", CodexRpcError("Token belongs to a different source or session", -32600)
	}
	return fields[4], nil
}

func (v *adapterView) projectAccess(id string, value protocol.SessionAccess) (*protocol.SessionAccess, error) {
	if value.NativeScopeID != v.scope || !opaquePattern.MatchString(value.CanonicalSessionRef) || value.Binding == "" || len(value.Binding) > 4096 || strings.ContainsAny(value.Binding, "\x00\r\n") {
		return nil, errors.New("Unverified native session access")
	}
	caps, err := normalizedCaps(value.Capabilities)
	if err != nil {
		return nil, err
	}
	value.Capabilities = intersectCaps(caps, v.Capabilities)
	value.Binding = v.seal("binding", id, value.Binding)
	return &value, nil
}

func (v *adapterView) access(ctx context.Context, id string) (*protocol.SessionAccess, error) {
	provider, ok := v.Sessions.(SessionAccessProvider)
	if !ok {
		return nil, nil
	}
	if err := v.check(ctx); err != nil {
		return nil, err
	}
	value, err := provider.Access(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := v.check(ctx); err != nil {
		return nil, err
	}
	return v.projectAccess(id, value)
}

func (s *Server) AdapterChanged(adapterID, sessionID string) {
	a := s.adapters[adapterID]
	if a == nil {
		return
	}
	v, err := a.view(context.Background())
	if err == nil {
		s.events.Changed(v.key(sessionID))
	}
}

func (s *Server) AdapterReset(adapterID string) {
	if s.adapters[adapterID] != nil {
		s.events.Reset()
	}
}

func intersectCaps(c, allowed protocol.AdapterCapabilities) protocol.AdapterCapabilities {
	c.Create = c.Create && allowed.Create
	c.History = c.History && allowed.History
	c.Live = c.Live && allowed.Live
	c.IdleSend = c.IdleSend && allowed.IdleSend
	if allowed.ActiveSend != "native-steer" {
		c.ActiveSend = "unsupported"
	}
	c.Stop = c.Stop && allowed.Stop
	c.Rename = c.Rename && allowed.Rename
	c.Archive = c.Archive && allowed.Archive
	c.Uploads = c.Uploads && allowed.Uploads
	c.Models = c.Models && allowed.Models
	c.Usage = c.Usage && allowed.Usage
	c.LazyMessages = c.LazyMessages && allowed.LazyMessages
	if allowed.HistoryMode != "complete" {
		c.HistoryMode = "bounded"
	}
	fields := []string{}
	for _, field := range c.Settings {
		for _, permitted := range allowed.Settings {
			if field == permitted {
				fields = append(fields, field)
			}
		}
	}
	c.Settings = fields
	return c
}

func (s *Server) routeAdapter(w *tracking, r *http.Request) error {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/adapters/")
	id, tail, found := strings.Cut(rest, "/")
	a := s.adapters[id]
	if !found || a == nil {
		return s.respond(w, http.StatusNotFound, errorResponse{Error: "Adapter not found"})
	}
	if a.Sessions == nil {
		return s.respond(w, http.StatusServiceUnavailable, errorResponse{Error: "Configured adapter is unavailable"})
	}
	v, err := a.view(r.Context())
	if err != nil {
		return s.respond(w, http.StatusServiceUnavailable, errorResponse{Error: "Native source identity is unavailable"})
	}
	return v.route(w, r, "/v1/"+tail)
}
