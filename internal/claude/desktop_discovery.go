package claude

import (
	"errors"
	"strings"
)

// Names are bounded diagnostic evidence, never a trusted host or control grant.
// tool.list provides no native server provenance or input schema.
type desktopDiscovery struct {
	Status string   `json:"status"`
	Tools  []string `json:"tools"`
}

func knownDesktopTool(name string) bool {
	const prefix = "mcp__ccd_session_mgmt__"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	switch strings.TrimPrefix(name, prefix) {
	case "list_sessions", "get_session", "list_events", "set_session_title", "archive_session", "unarchive_session",
		"send_message", "stop_session", "set_session_model", "set_session_effort", "start_session", "delete_session":
		return true
	}
	return false
}

func validateDesktopDiscovery(e modEvent) error {
	if e.Kind != "discovery" || e.Discovery == nil || e.TranscriptPath != "" || e.Block != nil || e.Title != nil ||
		e.ObservedCommand != "" || e.UUID != "" || e.Role != "" || e.Text != "" || e.TurnID != "" || e.Aborted || e.Dropped ||
		e.CanInput != nil || e.CanSettings != nil || e.CanStop != nil || e.ModelOptions != nil || e.Model != nil ||
		e.ContextTokens != nil || e.ContextWindow != nil || e.Effort != nil {
		return errors.New("Discovery cannot include control, runtime or transcript fields")
	}
	d := e.Discovery
	if d.Tools == nil || len(d.Tools) > 12 {
		return errors.New("Invalid bounded Desktop discovery")
	}
	switch d.Status {
	case "unverified":
	case "unavailable", "invalid", "timeout":
		if len(d.Tools) != 0 {
			return errors.New("Failed discovery cannot retain tools")
		}
	default:
		return errors.New("Discovery cannot assert trusted provenance")
	}
	previous := ""
	for _, name := range d.Tools {
		if !knownDesktopTool(name) || name <= previous {
			return errors.New("Invalid or duplicate Desktop discovery tool")
		}
		previous = name
	}
	return nil
}

func cloneDesktopDiscovery(d *desktopDiscovery) *desktopDiscovery {
	if d == nil {
		return &desktopDiscovery{Status: "not-observed", Tools: []string{}}
	}
	return &desktopDiscovery{Status: d.Status, Tools: append([]string{}, d.Tools...)}
}
