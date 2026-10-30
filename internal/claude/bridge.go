package claude

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/newo-ether/filo/internal/protocol"
)

type registerRequest struct {
	SessionID string `json:"sessionId"`
	Instance  string `json:"instance"`
	Cwd       string `json:"cwd"`
	Origin    string `json:"origin"`
	Version   string `json:"version"`
}

type envelope struct {
	SessionID string            `json:"sessionId"`
	Nonce     string            `json:"nonce"`
	Sequence  uint64            `json:"sequence,omitempty"`
	Events    []modEvent        `json:"events,omitempty"`
	Result    *modCommandResult `json:"result,omitempty"`
}

type modEvent struct {
	TranscriptPath  string         `json:"transcriptPath,omitempty"`
	Block           *modBlock      `json:"block,omitempty"`
	Title           *protocol.Text `json:"title,omitempty"`
	ObservedCommand string         `json:"observedCommand,omitempty"`
	Kind            string         `json:"kind"`
	UUID            string         `json:"uuid,omitempty"`
	Role            string         `json:"role,omitempty"`
	Text            protocol.Text  `json:"text,omitempty"`
	TurnID          string         `json:"turnId,omitempty"`
	Aborted         bool           `json:"aborted,omitempty"`
	// Lost rows are disclosed, never converted to invented message identities.
	Dropped       bool     `json:"dropped,omitempty"`
	CanInput      *bool    `json:"canInput,omitempty"`
	CanSettings   *bool    `json:"canSettings,omitempty"`
	CanStop       *bool    `json:"canStop,omitempty"`
	ModelOptions  []string `json:"modelOptions,omitempty"`
	Model         *string  `json:"model,omitempty"`
	ContextTokens *float64 `json:"contextTokens,omitempty"`
	ContextWindow *float64 `json:"contextWindow,omitempty"`
	Effort        *string  `json:"effort,omitempty"`
}

type bridgeResponse struct {
	Nonce           string `json:"nonce,omitempty"`
	TTLMillis       int64  `json:"ttlMillis,omitempty"`
	Sequence        uint64 `json:"sequence,omitempty"`
	Commands        []any  `json:"commands,omitempty"`
	ReadOnly        bool   `json:"readOnly"`
	HistoryBoundary string `json:"historyBoundary"`
	Error           string `json:"error,omitempty"`
}

// ServeHTTP must be mounted on the helper's distinct loopback listener. A public
// mount still refuses non-loopback callers; its token is not the gateway bearer.
func (a *Adapter) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	ip := net.ParseIP(host)
	expected := "Bearer " + a.token
	provided := request.Header.Get("Authorization")
	if err != nil || ip == nil || !ip.IsLoopback() || request.Header.Get("Origin") != "" ||
		len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		a.respond(w, http.StatusUnauthorized, bridgeResponse{Error: "Unauthorized"})
		return
	}
	if request.Method != http.MethodPost || !strings.HasPrefix(request.URL.Path, bridgePrefix) {
		a.respond(w, http.StatusNotFound, bridgeResponse{Error: "Not found"})
		return
	}
	path := strings.TrimPrefix(request.URL.Path, bridgePrefix)
	if path == "register" {
		var input registerRequest
		if decodeRequest(request, &input) != nil {
			a.respond(w, http.StatusBadRequest, bridgeResponse{Error: "Invalid registration"})
			return
		}
		a.register(w, input)
		return
	}
	var input envelope
	if decodeRequest(request, &input) != nil || !nativeUUID.MatchString(input.SessionID) || !opaque.MatchString(input.Nonce) {
		a.respond(w, http.StatusBadRequest, bridgeResponse{Error: "Invalid Mod envelope"})
		return
	}
	if path == "pull" {
		a.pull(w, input)
		return
	}
	if path != "result" && path != "push" && path != "revoke" {
		a.respond(w, http.StatusNotFound, bridgeResponse{Error: "Not found"})
		return
	}
	a.mu.Lock()
	r, err := a.findLocked(input.SessionID)
	if err != nil || r.nonce != input.Nonce {
		a.mu.Unlock()
		a.respond(w, http.StatusConflict, bridgeResponse{Error: "Claude Mod registration changed"})
		return
	}
	result := bridgeResponse{ReadOnly: true, TTLMillis: a.ttl.Milliseconds(), Sequence: r.sequence,
		HistoryBoundary: HistoryBoundary}
	status := http.StatusOK
	switch path {
	case "result":
		err = a.commandResultLocked(r, input)
	case "revoke":
		notifyModCommand(r)
		delete(a.registrations, input.SessionID)
	case "push":
		if input.Result != nil {
			err = errors.New("Event batch cannot include a command result")
		} else {
			err = a.pushLocked(r, input)
		}
		result.Sequence = r.sequence
	}
	if r.dropped {
		result.HistoryBoundary = "observation-window-truncated"
	}
	if err != nil {
		status = http.StatusConflict
		result.Error = err.Error()
	}
	a.mu.Unlock()
	if err == nil && path != "pull" {
		a.changed(input.SessionID)
	}
	a.respond(w, status, result)
}

func decodeRequest(request *http.Request, target any) error {
	data, err := io.ReadAll(io.LimitReader(request.Body, maxBody+1))
	if err != nil || len(data) > maxBody || !utf8.Valid(data) {
		return errors.New("Invalid bounded body")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("Trailing JSON")
	}
	return nil
}

func (a *Adapter) respond(w http.ResponseWriter, status int, response bridgeResponse) {
	if status == http.StatusOK && response.HistoryBoundary == "" {
		response.ReadOnly = true
		response.HistoryBoundary = HistoryBoundary
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func (a *Adapter) register(w http.ResponseWriter, input registerRequest) {
	if !nativeUUID.MatchString(input.SessionID) || !opaque.MatchString(input.Instance) ||
		len(input.Cwd) > 4096 || len(input.Version) > 128 || (input.Origin != "terminal" && input.Origin != "desktop") {
		a.respond(w, http.StatusBadRequest, bridgeResponse{Error: "Invalid native Mod identity"})
		return
	}
	if input.Origin != a.origin {
		a.respond(w, http.StatusForbidden, bridgeResponse{Error: "Claude Mod origin is not configured"})
		return
	}
	nonce, err := randomNonce()
	if err != nil {
		a.respond(w, http.StatusInternalServerError, bridgeResponse{Error: "Registration unavailable"})
		return
	}
	a.mu.Lock()
	a.pruneLocked()
	old := a.registrations[input.SessionID]
	if old != nil && old.instance != input.Instance || old == nil && len(a.registrations) >= maxSessions {
		a.mu.Unlock()
		a.respond(w, http.StatusConflict, bridgeResponse{Error: "Native session already registered or bridge is full"})
		return
	}
	notifyModCommand(old)
	// Every registration rotates scope, including reconnect/reload. No account
	// proof is available to coalesce identities across registrations.
	a.registrations[input.SessionID] = &registration{nativeID: input.SessionID, nonce: nonce,
		instance: input.Instance, canonical: a.canonical(input.SessionID, nonce), cwd: input.Cwd,
		origin: input.Origin, version: input.Version, expires: a.now().Add(a.ttl), registered: a.now(), updated: a.now(), status: "unknown"}
	a.mu.Unlock()
	a.changed(input.SessionID)
	a.respond(w, http.StatusOK, bridgeResponse{Nonce: nonce, TTLMillis: a.ttl.Milliseconds(),
		ReadOnly: true, HistoryBoundary: HistoryBoundary})
}

func (a *Adapter) pushLocked(r *registration, input envelope) error {
	if len(input.Events) == 0 || len(input.Events) > maxEvents {
		return errors.New("Invalid bounded event batch")
	}
	data, _ := json.Marshal(input.Events)
	digest := sha256.Sum256(data)
	if input.Sequence == r.sequence && input.Sequence != 0 && digest == r.lastDigest {
		return nil
	}
	if input.Sequence != r.sequence+1 {
		return errors.New("Mod event sequence changed")
	}
	for _, event := range input.Events {
		if err := validateEvent(event); err != nil {
			return err
		}
	}
	// Apply a validated batch atomically, including duplicate UUID validation.
	working := *r
	working.rows = append([]observedRow(nil), r.rows...)
	working.inputs = append([]modInput(nil), r.inputs...)
	if r.settingsChange != nil {
		change := *r.settingsChange
		working.settingsChange = &change
	}
	for _, event := range input.Events {
		switch event.Kind {
		case "row":
			if err := a.appendLocked(&working, event); err != nil {
				return err
			}
		case "snapshot":
			working.canStop = event.CanStop != nil && *event.CanStop
			working.canInput = event.CanInput != nil && *event.CanInput
			working.canSettings = event.CanSettings != nil && *event.CanSettings
			working.modelOptions = append([]string(nil), event.ModelOptions...)
			working.model, working.contextTokens, working.contextWindow = event.Model, event.ContextTokens, event.ContextWindow
			working.effort = event.Effort
		case "request":
			if working.activeTurn != event.TurnID {
				continue
			}
			working.requestRevision++
			working.model, working.effort = event.Model, event.Effort
			if change := working.settingsChange; change != nil && change.State == "pending" && change.ID == event.ObservedCommand && event.TurnID != working.settingsAfterTurn {
				change.TurnID = event.TurnID
				if event.Model != nil {
					change.ActualModel = *event.Model
				}
				if event.Effort != nil {
					change.ActualEffort = *event.Effort
				}
				change.State = "confirmed"
				if change.Requested.Model.Known && change.Requested.Model.Value != change.ActualModel ||
					change.Requested.Effort.Known && change.Requested.Effort.Value != change.ActualEffort {
					change.State = "failed"
				}
			}
		case "block":
			if err := a.projectBlock(&working, event); err != nil {
				return err
			}
		case "metadata":
			working.transcript = event.TranscriptPath
		case "title":
			working.title = event.Title
		case "start":
			working.activeTurn, working.status = event.TurnID, "active"
			working.activeHasUser = strings.TrimSpace(string(event.Text)) != ""
		case "complete":
			finishObservedBlocks(&working, event.TurnID, event.Aborted)
			if working.activeTurn == event.TurnID {
				working.activeTurn, working.completedTurn, working.status = "", event.TurnID, "idle"
			}
		case "truncated":
			working.dropped = true
		case "gap":
			working.dropped, working.status = true, "unknown"
		case "busy":
			working.status = "unknown"
		}
		for i := range working.inputs {
			attempt := &working.inputs[i]
			if event.ObservedCommand != attempt.CommandID || attempt.AfterSequence >= input.Sequence || string(event.Text) != attempt.Text {
				continue
			}
			if event.Kind == "row" && event.Role == "user" || event.Kind == "start" {
				attempt.Matched = true
				attempt.Receipt.MessageID, attempt.Receipt.TurnID = event.UUID, event.TurnID
				if attempt.Receipt.State == "submitted" || attempt.Accepted && attempt.Receipt.State == "unconfirmed" {
					attempt.Receipt.State = "observed"
				}
			}
		}
	}
	working.sequence, working.lastDigest, working.updated, working.expires = input.Sequence, digest, a.now(), a.now().Add(a.ttl)
	*r = working
	if r.pending != nil {
		for _, event := range input.Events {
			if event.Kind == "complete" && event.TurnID == r.pending.command.TurnID && r.pending.delivered {
				r.pending.completed = true
			}
		}
	}
	notifyModCommand(r)
	return nil
}

func validateEvent(e modEvent) error {
	if e.TranscriptPath != "" && (e.Kind != "metadata" || len(e.TranscriptPath) > 4096) {
		return errors.New("Invalid native metadata source")
	}
	if e.Block != nil && e.Kind != "block" {
		return errors.New("Block fields require a native block event")
	}
	if e.Kind == "block" {
		return validateBlock(e)
	}
	if e.Title != nil && (e.Kind != "title" || len(*e.Title) > 4096) {
		return errors.New("Invalid bounded native title")
	}
	if e.Kind == "title" && e.Title == nil {
		return errors.New("Native title required")
	}
	if len(e.Text) > maxRowBytes {
		return errors.New("Native event text exceeds bound")
	}
	if e.ObservedCommand != "" && !opaque.MatchString(e.ObservedCommand) {
		return errors.New("Invalid observed command identity")
	}
	if e.ObservedCommand != "" && e.Kind != "request" && e.Kind != "row" && e.Kind != "start" {
		return errors.New("Invalid input observation event")
	}
	if e.Kind != "snapshot" && e.Kind != "request" && (e.CanInput != nil || e.CanSettings != nil || e.CanStop != nil || e.ModelOptions != nil || e.Model != nil || e.ContextTokens != nil || e.ContextWindow != nil || e.Effort != nil) {
		return errors.New("Runtime fields require a snapshot event")
	}
	switch e.Kind {
	case "request":
		if e.UUID != "" || e.Role != "" || e.Text != "" || e.Aborted || e.Dropped || e.CanInput != nil || e.CanSettings != nil || e.CanStop != nil || e.ModelOptions != nil || e.ContextTokens != nil || e.ContextWindow != nil {
			return errors.New("Request cannot include snapshot or transcript fields")
		}
		if !opaque.MatchString(e.TurnID) || e.Model == nil || !validNativeModel(*e.Model) ||
			e.Effort != nil && !validObservedEffort(*e.Effort) {
			return errors.New("Invalid native request evidence")
		}
	case "snapshot":
		if e.UUID != "" || e.Role != "" || e.Text != "" || e.TurnID != "" || e.Aborted || e.Dropped {
			return errors.New("Snapshot cannot include transcript or turn fields")
		}
		if e.Model != nil && (*e.Model == "" || len(*e.Model) > 256 || strings.TrimSpace(*e.Model) != *e.Model ||
			strings.IndexFunc(*e.Model, unicode.IsControl) >= 0) {
			return errors.New("Invalid bounded native model")
		}
		if e.Effort != nil && !validObservedEffort(*e.Effort) {
			return errors.New("Invalid native effort reading")
		}
		if err := validateModelOptions(e.ModelOptions); err != nil {
			return err
		}
		for _, value := range []*float64{e.ContextTokens, e.ContextWindow} {
			if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 ||
				*value > 9007199254740991 || math.Trunc(*value) != *value) {
				return errors.New("Native context counts must be nonnegative safe integers")
			}
		}
		if e.ContextWindow != nil && *e.ContextWindow == 0 {
			return errors.New("Native context window must be positive")
		}
	case "row":
		if e.ObservedCommand != "" && e.Role != "user" {
			return errors.New("Only native user rows can observe input")
		}
		if !nativeUUID.MatchString(e.UUID) || (e.Role != "user" && e.Role != "assistant") || len(e.Text) > maxRowBytes || e.TurnID != "" {
			return errors.New("Native row UUID/role/bound required; no inferred row turn")
		}
	case "start", "complete":
		if !opaque.MatchString(e.TurnID) {
			return errors.New("Native turn identity required")
		}
	case "metadata", "title", "truncated", "gap", "busy":
	default:
		return errors.New("Unsupported Mod event")
	}
	return nil
}

func (a *Adapter) appendLocked(r *registration, e modEvent) error {
	for _, row := range r.rows {
		if row.UUID == e.UUID {
			if row.Role != e.Role || row.Text != e.Text {
				return errors.New("Native row UUID content changed")
			}
			return nil
		}
	}
	r.rows = append(r.rows, observedRow{UUID: e.UUID, NativeID: e.UUID, Role: e.Role, Text: e.Text})
	r.rowBytes += len(e.Text)
	trimObservedRows(r)
	return nil
}
