package codex

import (
	"github.com/newo-ether/filo/internal/toolpreview"

	"github.com/newo-ether/filo/internal/protocol"
)

// ParseSettings accepts only the three next-turn presentation keys. A null
// serviceTier means the native default and is the sole permitted null value.
func ParseSettings(value map[string]any) (protocol.SessionSettings, error) {
	var out protocol.SessionSettings
	if len(value) == 0 {
		return out, settingsError()
	}
	for key, raw := range value {
		switch key {
		case "model", "effort", "serviceTier":
		default:
			return out, settingsError()
		}
		if key == "serviceTier" && raw == nil {
			continue
		}
		text, ok := raw.(string)
		if !ok || text == "" || utf16Len(text) > 128 {
			return out, settingsError()
		}
	}
	if text, ok := value["model"].(string); ok {
		out.Model = protocol.Known(text)
	}
	if text, ok := value["effort"].(string); ok {
		out.Effort = protocol.Known(text)
	}
	if raw, has := value["serviceTier"]; has {
		if raw == nil {
			out.ServiceTier = protocol.Known[*string](nil)
		} else {
			text := raw.(string)
			out.ServiceTier = protocol.Known(&text)
		}
	}
	return out, nil
}

// ValidateSettings revalidates structure then checks the model catalog.
// Absent effort and null service tier stay valid; unlisted values never pass.
func ValidateSettings(settings protocol.SessionSettings, models []protocol.Model, currentModel *string) error {
	if !settings.Model.Known && !settings.Effort.Known && !settings.ServiceTier.Known {
		return settingsError()
	}
	if settings.Model.Known && (settings.Model.Value == "" || utf16Len(settings.Model.Value) > 128) {
		return settingsError()
	}
	if settings.Effort.Known && (settings.Effort.Value == "" || utf16Len(settings.Effort.Value) > 128) {
		return settingsError()
	}
	if settings.ServiceTier.Known && settings.ServiceTier.Value != nil &&
		(*settings.ServiceTier.Value == "" || utf16Len(*settings.ServiceTier.Value) > 128) {
		return settingsError()
	}
	name := currentModel
	if settings.Model.Known {
		own := settings.Model.Value
		name = &own
	}
	if name == nil {
		return settingsErrorWithMessage("Model is unavailable")
	}
	var model *protocol.Model
	for index := range models {
		if models[index].ID == *name {
			model = &models[index]
			break
		}
	}
	if model == nil {
		return settingsErrorWithMessage("Model is unavailable")
	}
	if settings.Effort.Known && !contains(model.ReasoningEfforts, settings.Effort.Value) {
		return settingsErrorWithMessage("Thinking level is unavailable for this model")
	}
	if settings.ServiceTier.Known && settings.ServiceTier.Value != nil {
		found := false
		for _, tier := range model.ServiceTiers {
			if tier.ID == *settings.ServiceTier.Value {
				found = true
				break
			}
		}
		if !found {
			return settingsErrorWithMessage("Service tier is unavailable for this model")
		}
	}
	return nil
}

func settingsError() *RpcError {
	return settingsErrorWithMessage("Expected model, effort or serviceTier settings")
}

func settingsErrorWithMessage(message string) *RpcError {
	code := float64(-32602)
	return &RpcError{Message: message, Code: &code}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func utf16Len(value string) int {
	return toolpreview.UTF16Len(value)
}
