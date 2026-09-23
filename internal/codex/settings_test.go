package codex

import (
	"errors"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

var settingsCatalog = []protocol.Model{{
	ID: "model", Name: "Model", IsDefault: true,
	ReasoningEfforts:       []string{"low", "ultra"},
	DefaultReasoningEffort: protocol.Known(text("low")),
	ServiceTiers:           []protocol.ServiceTier{{ID: "priority", Name: "Fast", Description: "Faster"}},
	DefaultServiceTier:     protocol.Known[*string](nil),
}}

func text(value string) *string { return &value }

func TestParseSettingsAcceptsOnlyVerifiedKeys(t *testing.T) {
	effort, err := ParseSettings(map[string]any{"effort": "low"})
	if err != nil || !effort.Effort.Known || effort.Effort.Value != "low" ||
		effort.Model.Known || effort.ServiceTier.Known {
		t.Fatalf("effort parse = %+v, %v", effort, err)
	}
	tier, err := ParseSettings(map[string]any{"serviceTier": nil})
	if err != nil || !tier.ServiceTier.Known || tier.ServiceTier.Value != nil {
		t.Fatalf("null tier parse = %+v, %v", tier, err)
	}
	for _, rejected := range []map[string]any{
		{},
		{"effort": nil},
		{"model": ""},
		{"serviceTier": 1.0},
		{"sandbox": "danger-full-access"},
	} {
		if _, err := ParseSettings(rejected); err == nil {
			t.Fatalf("accepted %v", rejected)
		} else if !strings.Contains(err.Error(), "Expected") {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestValidateSettingsModelCapabilities(t *testing.T) {
	full := protocol.SessionSettings{
		Effort:      protocol.Known("ultra"),
		ServiceTier: protocol.Known(text("priority")),
	}
	if err := ValidateSettings(full, settingsCatalog, text("model")); err != nil {
		t.Fatalf("full settings rejected: %v", err)
	}
	nativeDefault := protocol.SessionSettings{ServiceTier: protocol.Known[*string](nil)}
	if err := ValidateSettings(nativeDefault, settingsCatalog, text("model")); err != nil {
		t.Fatalf("native default tier rejected: %v", err)
	}
	cases := []struct {
		settings protocol.SessionSettings
		current  *string
		want     string
	}{
		{protocol.SessionSettings{Effort: protocol.Known("none")}, text("model"), "Thinking"},
		{protocol.SessionSettings{ServiceTier: protocol.Known(text("fast"))}, text("model"), "Service tier"},
		{protocol.SessionSettings{Effort: protocol.Known("low")}, text("missing"), "Model is unavailable"},
		{protocol.SessionSettings{Effort: protocol.Known("low")}, nil, "Model is unavailable"},
	}
	for _, testCase := range cases {
		err := ValidateSettings(testCase.settings, settingsCatalog, testCase.current)
		var rpcError *RpcError
		if !errors.As(err, &rpcError) || !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("want %q, got %v", testCase.want, err)
		}
	}
}

func TestSettingsLengthBoundary(t *testing.T) {
	ok := strings.Repeat("a", 128)
	if _, err := ParseSettings(map[string]any{"effort": ok}); err != nil {
		t.Fatalf("128-unit effort rejected: %v", err)
	}
	if _, err := ParseSettings(map[string]any{"effort": strings.Repeat("a", 129)}); err == nil {
		t.Fatal("129-unit effort accepted")
	}
}
