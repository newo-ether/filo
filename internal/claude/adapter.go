// Package claude observes explicitly opted-in official Claude Code Mods. It never
// spawns a native session, injects terminal input or manufactures input receipts.
package claude

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

const (
	DefaultTTL      = 15 * time.Second
	maxSessions     = 16
	maxBody         = 64 << 10
	maxRows         = 128
	maxSessionBytes = 256 << 10
	maxRowBytes     = 24 << 10
	maxEvents       = 24
	bridgePrefix    = "/claude-mod/v1/"
)

var opaque = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
var nativeUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var tokenPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// Config belongs to the existing selected-user helper. Token is a distinct
// private Mod secret, never the public service bearer. Origin selects one native surface.
type Config struct {
	Token         string
	NativeScopeID string
	Origin        string
	TTL           time.Duration
	OnChange      func(nativeSessionID string)
}

type BridgeStatus struct {
	State string
	Error *string
}

// Adapter is both the native SessionAPI and the authenticated loopback handler.
// State is bounded in memory and discarded on helper restart. Conversation
// history reads only native records through a source-bound lifecycle transcript
// path. Native transcripts are never written. Separate registrations cannot assert account continuity.
type Adapter struct {
	mu            sync.Mutex
	token         string
	scope         string
	origin        string
	ttl           time.Duration
	onChange      func(string)
	now           func() time.Time
	registrations map[string]*registration
}

type registration struct {
	nativeID          string
	nonce             string
	instance          string
	canonical         string
	cwd               string
	origin            string
	version           string
	expires           time.Time
	registered        time.Time
	updated           time.Time
	sequence          uint64
	lastDigest        [32]byte
	rows              []observedRow
	rowBytes          int
	dropped           bool
	status            string
	activeTurn        string
	activeHasUser     bool
	title             *protocol.Text
	transcript        string
	metadata          string
	completedTurn     string
	modelOptions      []string
	model             *string
	effort            *string
	contextTokens     *float64
	contextWindow     *float64
	canStop           bool
	canInput          bool
	canSettings       bool
	inputs            []modInput
	settingsAfterTurn string
	settingsChange    *protocol.SettingsChange
	requestRevision   uint64
	pending           *pendingModCommand
}

type observedRow struct {
	UUID     string        `json:"uuid"`
	Role     string        `json:"role"`
	Text     protocol.Text `json:"text"`
	TurnID   string
	NativeID string
	Activity *protocol.Activity
}

func New(config Config) (*Adapter, error) {
	if config.Origin == "" {
		config.Origin = "terminal"
	}
	if config.Origin != "terminal" && config.Origin != "desktop" {
		return nil, errors.New("Claude Mod bridge requires a terminal or desktop origin")
	}
	if !tokenPattern.MatchString(config.Token) || !opaque.MatchString(config.NativeScopeID) {
		return nil, errors.New("Claude Mod bridge requires a private 256-bit token and opaque native scope")
	}
	if config.TTL == 0 {
		config.TTL = DefaultTTL
	}
	if config.TTL < 3*time.Second || config.TTL > time.Minute {
		return nil, errors.New("Claude Mod bridge TTL must be between 3 and 60 seconds")
	}
	return &Adapter{token: config.Token, scope: config.NativeScopeID, origin: config.Origin, ttl: config.TTL,
		onChange: config.OnChange, now: time.Now, registrations: make(map[string]*registration)}, nil
}

// Capabilities is the source ceiling. Native Stop still requires a live Mod
// registration, its abort API, an exact active turn and terminal confirmation.
func Capabilities() protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{History: true, Live: true, Stop: true, Models: true, IdleSend: true, ActiveSend: "native-steer",
		Settings: []string{"model", "effort"}, HistoryMode: "bounded", LazyMessages: true}
}

func (a *Adapter) Descriptor() protocol.AdapterDescriptor {
	status := a.Status()
	id, origin := "claude-code", "cli"
	if a.origin == "desktop" {
		id, origin = "claude-desktop", "desktop"
	}
	return protocol.AdapterDescriptor{ID: id, Family: "claude", Origin: origin,
		State: status.State, Error: status.Error, NativeScopeID: a.scope, Capabilities: Capabilities()}
}

func (a *Adapter) Status() BridgeStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	if len(a.registrations) > 0 {
		return BridgeStatus{State: "connected"}
	}
	reason := "claude_mod_not_registered"
	return BridgeStatus{State: "unavailable", Error: &reason}
}

func (a *Adapter) pruneLocked() {
	for id, r := range a.registrations {
		if !a.now().Before(r.expires) {
			notifyModCommand(r)
			delete(a.registrations, id)
		}
	}
}

func (a *Adapter) findLocked(id string) (*registration, error) {
	a.pruneLocked()
	r := a.registrations[id]
	if r == nil {
		return nil, errors.New("Claude Mod session is no longer registered")
	}
	return r, nil
}

func (a *Adapter) changed(id string) {
	if a.onChange != nil {
		a.onChange(id)
	}
}

func randomNonce() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func (a *Adapter) canonical(id, nonce string) string {
	mac := hmac.New(sha256.New, []byte(a.token))
	mac.Write([]byte(a.scope + "\x00" + id + "\x00" + nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Adapter) accessLocked(r *registration) protocol.SessionAccess {
	return protocol.SessionAccess{NativeScopeID: a.scope, CanonicalSessionRef: r.canonical,
		Binding: r.nonce, Capabilities: a.registrationCapabilities(r)}
}
