package codex

import (
	"errors"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// applySettings waits for native applied evidence, never treating an empty RPC
// acknowledgement as read-back and never replaying a patch after uncertainty.
func (s *NativeTaskSession) applySettings(id string, params map[string]any, patch protocol.SessionSettings) error {
	s.mu.Lock()
	version, attachment := s.settingsVersion[id], s.attached[id]
	s.mu.Unlock()
	result, err := s.rpc.Request("thread/settings/update", params)
	if err != nil {
		return err
	}
	fields, _ := nativejson.Fields(result)
	if values, ok := nativejson.Fields(fields["threadSettings"]); ok {
		if nativejson.Text(fields["threadId"]) != id {
			return errors.New("Native settings identity mismatch")
		}
		s.Receive("thread/settings/updated", map[string]any{"threadId": id, "threadSettings": values})
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		s.mu.Lock()
		current, present := s.settings[id]
		changed := s.settingsChanged
		currentVersion := s.settingsVersion[id]
		attached := attachment != nil && s.attached[id] == attachment
		s.mu.Unlock()
		if attached && present && currentVersion > version && settingsMatch(current, patch) {
			return nil
		}
		if !attached {
			return errors.New("Native settings outcome is unconfirmed")
		}
		select {
		case <-changed:
		case <-timer.C:
			return errors.New("Native settings outcome is unconfirmed")
		}
	}
}

func settingsMatch(current nativeSessionSettings, patch protocol.SessionSettings) bool {
	if patch.Model.Known && (current.Model == nil || *current.Model != patch.Model.Value) {
		return false
	}
	if patch.Effort.Known && (current.Effort == nil || *current.Effort != patch.Effort.Value) {
		return false
	}
	if patch.ServiceTier.Known {
		if !current.ServiceTier.Known {
			return false
		}
		if patch.ServiceTier.Value == nil || current.ServiceTier.Value == nil {
			return patch.ServiceTier.Value == nil && current.ServiceTier.Value == nil
		}
		return *patch.ServiceTier.Value == *current.ServiceTier.Value
	}
	return true
}
