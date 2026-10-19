package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

// Each field records native evidence independently of unrelated conversation patches.
type desktopSettingsEvidence struct {
	model, effort, tier int64
}

func (e desktopSettingsEvidence) confirms(revision int64, patch protocol.SessionSettings) bool {
	return (!patch.Model.Known || e.model > revision) &&
		(!patch.Effort.Known || e.effort > revision) &&
		(!patch.ServiceTier.Known || e.tier > revision)
}

func (entry *followed) recordSettingsEvidence(change map[string]any) {
	if text(change["type"]) == "snapshot" {
		entry.settingsEvidence = desktopSettingsEvidence{entry.revision, entry.revision, entry.revision}
		return
	}
	var model, effort, fallbackEffort, tier bool
	patches, _ := change["patches"].([]any)
	for _, value := range patches {
		path, _ := fields(value)["path"].([]any)
		if len(path) == 0 {
			model, effort, fallbackEffort, tier = true, true, true, true
			continue
		}
		switch text(path[0]) {
		case "latestModel":
			model = true
		case "latestReasoningEffort":
			fallbackEffort = true
		case "latestThreadSettings":
			if len(path) == 1 {
				effort, tier = true, true
			} else {
				switch text(path[1]) {
				case "effort":
					effort = true
				case "serviceTier":
					tier = true
				}
			}
		}
	}
	if model {
		entry.settingsEvidence.model = entry.revision
	}
	// A patch of an unused fallback cannot refresh the projected effort value.
	usesSettingsEffort := fields(entry.state["latestThreadSettings"])["effort"] != nil
	if (usesSettingsEffort && effort) || (!usesSettingsEffort && fallbackEffort) {
		entry.settingsEvidence.effort = entry.revision
	}
	if tier {
		entry.settingsEvidence.tier = entry.revision
	}
}

// waitSettings requires fresh evidence for every requested field from the same
// native owner. An ACK and a message/usage patch cannot confirm cached settings.
func (sessions *DesktopSessions) waitSettings(ctx context.Context, entry *followed, revision int64, patch protocol.SessionSettings) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		sessions.f.mu.Lock()
		valid := !sessions.f.stopped && sessions.f.entries[entry.id] == entry && entry.err == nil
		state, evidence, current, updated := entry.state, entry.settingsEvidence, entry.revision, entry.updated
		sessions.f.mu.Unlock()
		if !valid {
			return errors.New(nativeSettingsUnconfirmed)
		}
		if current > revision && evidence.confirms(revision, patch) {
			page, err := codex.ProjectDesktop(state, false, 128)
			if err != nil {
				return err
			}
			if desktopSettingsMatch(page.Runtime, patch) {
				owner, err := sessions.f.owner(ctx, entry.id)
				if err != nil {
					return err
				}
				if owner == entry.owner {
					return nil
				}
				return errors.New(nativeSettingsUnconfirmed)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New(nativeSettingsUnconfirmed)
		case <-updated:
		}
	}
}

func desktopSettingsMatch(runtime *protocol.Runtime, patch protocol.SessionSettings) bool {
	if runtime == nil {
		return false
	}
	if patch.Model.Known && (runtime.Model == nil || *runtime.Model != patch.Model.Value) {
		return false
	}
	if patch.Effort.Known && (!runtime.Effort.Known || runtime.Effort.Value == nil || *runtime.Effort.Value != patch.Effort.Value) {
		return false
	}
	if patch.ServiceTier.Known {
		if !runtime.ServiceTier.Known {
			return false
		}
		if patch.ServiceTier.Value == nil || runtime.ServiceTier.Value == nil {
			return patch.ServiceTier.Value == nil && runtime.ServiceTier.Value == nil
		}
		return *patch.ServiceTier.Value == *runtime.ServiceTier.Value
	}
	return true
}
