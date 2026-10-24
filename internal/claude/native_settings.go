package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// validEffortLevels keeps only well-formed native effort identifiers.
func validEffortLevels(levels []string) []string {
	result := []string{}
	for _, level := range levels {
		if len(result) < 16 && len(level) > 0 && len(level) <= 32 && strings.TrimSpace(level) == level && !strings.ContainsAny(level, "\x00\r\n\t ") {
			result = append(result, level)
		}
	}
	return result
}

func (s *NativeSession) effortKnownLocked(effort string) bool {
	for _, model := range s.models {
		for _, level := range model.ReasoningEfforts {
			if level == effort {
				return true
			}
		}
	}
	return false
}

// SetEffort applies effortLevel through the official apply_flag_settings control
// and succeeds only when get_settings reports the same applied effort. A native
// environment override is reported as an error instead of a false success.
func (s *NativeSession) SetEffort(ctx context.Context, effort string) error {
	s.mu.Lock()
	if err := s.checkLocked(ctx); err != nil {
		s.mu.Unlock()
		return err
	}
	if !s.effortKnownLocked(effort) || s.active != nil || s.controlling {
		s.mu.Unlock()
		return errors.New("Claude effort is unavailable or session is busy")
	}
	s.controlling = true
	s.effort = ""
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.controlling = false; s.mu.Unlock() }()
	if _, err := s.control(ctx, map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": effort}}); err != nil {
		return err
	}
	response, err := s.control(ctx, map[string]any{"subtype": "get_settings"})
	if err != nil {
		return err
	}
	var applied struct {
		Effort *string `json:"effort"`
	}
	if json.Unmarshal(response["applied"], &applied) != nil || applied.Effort == nil {
		return errors.New("Claude effort readback is unavailable")
	}
	if *applied.Effort != effort {
		return errors.New("Claude native configuration overrides effort; " + *applied.Effort + " remains in effect")
	}
	s.mu.Lock()
	s.effort = effort
	s.signalLocked()
	s.mu.Unlock()
	s.notify()
	return nil
}

// Rename sets the native session title through the official rename_session control.
func (s *NativeSession) Rename(ctx context.Context, title string) error {
	if strings.TrimSpace(title) == "" || !utf8.ValidString(title) || len(title) > 1024 || strings.ContainsAny(title, "\x00\r\n") {
		return errors.New("Expected a task name")
	}
	s.mu.Lock()
	if err := s.checkLocked(ctx); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.controlling {
		s.mu.Unlock()
		return errors.New("Claude session is busy")
	}
	s.controlling = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.controlling = false; s.mu.Unlock() }()
	if _, err := s.control(ctx, map[string]any{"subtype": "rename_session", "title": title}); err != nil {
		return err
	}
	s.mu.Lock()
	s.title = title
	s.signalLocked()
	s.mu.Unlock()
	s.notify()
	return nil
}
