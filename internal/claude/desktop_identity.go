package claude

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// Desktop host IDs and engine UUIDs are different domains. Only the native
// record may associate them; neither prefix arithmetic nor caller input can.
type desktopIdentity struct {
	path, engineID, hostID string
}

func desktopHostID(id string) bool {
	return strings.HasPrefix(id, "local_") && nativeUUID.MatchString(strings.TrimPrefix(id, "local_"))
}

func readDesktopIdentity(path, engineID string) *desktopIdentity {
	var record struct {
		HostID   string `json:"sessionId"`
		EngineID string `json:"cliSessionId"`
	}
	if !nativeUUID.MatchString(engineID) || json.Unmarshal(readTitleFile(path, 256<<10), &record) != nil ||
		record.EngineID != engineID || !desktopHostID(record.HostID) || filepath.Base(path) != record.HostID+".json" {
		return nil
	}
	return &desktopIdentity{path: path, engineID: engineID, hostID: record.HostID}
}

func resolveDesktopIdentity(ctx context.Context, engineID string) *desktopIdentity {
	var found *desktopIdentity
	complete := walkDesktopMetadata(ctx, func(path string) bool {
		identity := readDesktopIdentity(path, engineID)
		if identity == nil {
			return true
		}
		if found != nil {
			return false // Ambiguous native records cannot qualify a host identity.
		}
		found = identity
		return true
	})
	if !complete {
		return nil
	}
	return found
}

func (identity *desktopIdentity) unchanged() bool {
	if identity == nil {
		return false
	}
	current := readDesktopIdentity(identity.path, identity.engineID)
	return current != nil && *current == *identity
}

func validDesktopReason(value *desktopMetadata) bool {
	if value.Reason == "" {
		return true
	}
	if value.Status != "invalid" {
		return false
	}
	switch value.Reason {
	case "result-shape", "text-missing", "text-too-large", "text-json", "metadata-shape",
		"host-session-mismatch", "title-shape", "mapping-unavailable":
		return true
	}
	return false
}

func validDesktopReadbackIdentity(r *registration, value *desktopMetadata) bool {
	identity := r.pending.desktopIdentity
	return identity != nil && identity.engineID == r.nativeID && value.SessionID == r.nativeID &&
		value.HostSessionID == identity.hostID && r.pending.command.HostSessionID == identity.hostID && identity.unchanged()
}
