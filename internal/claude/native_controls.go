package claude

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/newo-ether/filo/internal/protocol"
)

func ExecutionCapabilities() protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{History: true, Live: true, IdleSend: true, Stop: true, Models: true,
		ActiveSend: "unsupported", Settings: []string{"model", "effort"}, Rename: true, HistoryMode: "bounded", LazyMessages: true}
}

func (s *NativeSession) checkLocked(ctx context.Context) error {
	if err := protocol.ValidateScope(ctx); err != nil {
		return err
	}
	if binding := protocol.AccessBinding(ctx); binding != "" && binding != s.binding {
		return errors.New("Claude execution binding changed")
	}
	if s.exited || s.fenced {
		return errors.New("Claude native executor is unavailable; input was not replayed")
	}
	return nil
}

func (s *NativeSession) Send(ctx context.Context, text, client string) (protocol.SendReceipt, error) {
	if len(text) == 0 || len(text) > maxRowBytes || len(client) == 0 || len(client) > 256 || strings.ContainsAny(client, "\x00\r\n") {
		return protocol.SendReceipt{}, errors.New("Invalid Claude input or client identity")
	}
	s.mu.Lock()
	if err := s.checkLocked(ctx); err != nil {
		s.mu.Unlock()
		return protocol.SendReceipt{}, err
	}
	input := s.inputs[client]
	if input != nil {
		if input.text != text {
			s.mu.Unlock()
			return protocol.SendReceipt{}, errors.New("Claude client identity already has another input")
		}
		s.mu.Unlock()
		return waitNativeInput(ctx, input)
	}
	if s.active != nil || s.controlling {
		s.mu.Unlock()
		return protocol.SendReceipt{}, errors.New("Claude session is busy; input was not queued")
	}
	if len(s.inputs) >= 128 {
		s.mu.Unlock()
		return protocol.SendReceipt{}, errors.New("Claude execution receipt capacity reached")
	}
	input = &nativeInput{client: client, text: text, turn: ExecutionTurnPrefix + nativeID(), done: make(chan struct{}), terminal: make(chan struct{})}
	s.inputs[client] = input
	s.active = input
	s.signalLocked()
	s.mu.Unlock()
	if err := s.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}); err != nil {
		s.fence()
		return protocol.SendReceipt{}, errors.New("Claude input acceptance is unknown; do not replay it")
	}
	return waitNativeInput(ctx, input)
}

func waitNativeInput(ctx context.Context, input *nativeInput) (protocol.SendReceipt, error) {
	select {
	case <-ctx.Done():
		return protocol.SendReceipt{}, ctx.Err()
	case <-input.done:
		if input.failure != nil {
			return protocol.SendReceipt{}, input.failure
		}
		return protocol.SendReceipt{TurnID: input.turn, ClientID: input.client}, nil
	}
}

// Stop succeeds only after the exact interrupt request is acknowledged and a
// native result ends the same single admitted execution. Neither alone suffices.
func (s *NativeSession) Stop(ctx context.Context, turn string) error {
	s.mu.Lock()
	if err := s.checkLocked(ctx); err != nil {
		s.mu.Unlock()
		return err
	}
	input := s.active
	if input == nil || input.uuid == "" || input.turn != turn || s.controlling {
		s.mu.Unlock()
		return errors.New("Claude active execution changed")
	}
	s.controlling = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.controlling = false; s.mu.Unlock() }()
	if _, err := s.control(ctx, map[string]any{"subtype": "interrupt"}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-input.terminal:
		return nil
	}
}

// SetModel acknowledges the official set_model control. Runtime.Model remains
// unknown until the next native system/init reports the actual resolved model.
func (s *NativeSession) SetModel(ctx context.Context, model string) error {
	if !validNativeModel(model) {
		return errors.New("Invalid Claude model")
	}
	s.mu.Lock()
	if err := s.checkLocked(ctx); err != nil {
		s.mu.Unlock()
		return err
	}
	known := false
	for _, value := range s.models {
		if value.ID == model {
			known = true
		}
	}
	if !known || s.active != nil || s.controlling {
		s.mu.Unlock()
		return errors.New("Claude model is unavailable or session is busy")
	}
	s.controlling = true
	s.model = ""
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.controlling = false; s.mu.Unlock() }()
	if _, err := s.control(ctx, map[string]any{"subtype": "set_model", "model": model}); err != nil {
		return err
	}
	s.mu.Lock()
	s.signalLocked()
	s.mu.Unlock()
	s.notify()
	return nil
}

func (s *NativeSession) accessLocked() protocol.SessionAccess {
	caps := ExecutionCapabilities()
	caps.Create = false
	caps.IdleSend = !s.exited && !s.fenced && s.active == nil && !s.controlling
	caps.Stop = !s.exited && !s.fenced && s.active != nil && s.active.uuid != "" && !s.controlling
	if !caps.IdleSend {
		caps.Settings = []string{}
	}
	if s.exited || s.fenced {
		caps.Live = false
	}
	caps.Rename = caps.Live && !s.controlling
	caps.Uploads = s.options.UploadDirectory != "" && caps.IdleSend
	canonical := sha256.Sum256([]byte("claude\x00" + s.options.Scope + "\x00" + s.id))
	return protocol.SessionAccess{NativeScopeID: s.options.Scope, CanonicalSessionRef: base64.RawURLEncoding.EncodeToString(canonical[:]), Binding: s.binding, Capabilities: caps}
}

func (s *NativeSession) Access() protocol.SessionAccess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accessLocked()
}

func (s *NativeSession) Session() protocol.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	access := s.accessLocked()
	status := s.statusLocked()
	title := "Claude Code (Filo-owned)"
	if s.title != "" {
		title = s.title
	}
	return protocol.Session{ID: s.id, Title: protocol.Text(title), Cwd: s.options.Workspace,
		UpdatedAt: float64(s.updated.Unix()), Status: protocol.Known(&status), Access: &access}
}

func (s *NativeSession) statusLocked() string {
	if s.fenced || s.exited && s.active != nil {
		return "unknown"
	}
	if s.active != nil {
		return "active"
	}
	return "idle"
}

func (s *NativeSession) Read() protocol.ConversationPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	access := s.accessLocked()
	runtime := &protocol.Runtime{Status: s.statusLocked()}
	if s.title != "" {
		title := protocol.Text(s.title)
		runtime.Title = &title
	}
	if s.model != "" {
		model := s.model
		runtime.Model = &model
	}
	if s.effort != "" {
		effort := s.effort
		runtime.Effort = protocol.Known(&effort)
	}
	if s.active != nil {
		turn := s.active.turn
		runtime.ActiveTurnID = &turn
		runtime.ActiveTurnHasUserMessage = protocol.Known(s.active.uuid != "")
	}
	if s.completed != "" {
		turn := s.completed
		runtime.CompletedTurnID = protocol.Known(&turn)
	}
	return protocol.ConversationPage{Access: &access, Messages: nativeRowsSnapshot(s.rows), Queued: []protocol.QueuedInput{},
		Runtime: runtime, InputReceipts: append([]protocol.InputReceipt{}, s.receipts...)}
}
