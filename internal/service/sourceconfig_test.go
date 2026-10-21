package service

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func sourceConfigFixture(t *testing.T, text string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestConfiguredSourcesKeepLegacyDefaultAndExplicitOrder(t *testing.T) {
	for _, test := range []struct {
		text string
		want []string
	}{
		{`{}`, []string{codexDesktopSource}},
		{`{"sources":["claude-code"]}`, []string{claudeCodeSource}},
		{`{"sources":["codex-cli","codex-desktop","claude-code"]}`, []string{codexCLISource, codexDesktopSource, claudeCodeSource}},
	} {
		config := sourceConfigFixture(t, test.text)
		before, _ := json.Marshal(config)
		got, err := configuredSources(config)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("sources = %v, %v; want %v", got, err, test.want)
		}
		after, _ := json.Marshal(config)
		if string(before) != string(after) {
			t.Fatal("source admission rewrote the private configuration")
		}
	}
}

func TestConfiguredSourcesRejectAmbiguousOrUnconfiguredFallbacks(t *testing.T) {
	for _, text := range []string{
		`{"sources":null}`, `{"sources":[]}`, `{"sources":"codex-cli"}`,
		`{"sources":["codex-cli","codex-cli"]}`, `{"sources":["codex"]}`,
		`{"sources":["claude-desktop"]}`, `{"sources":[1]}`,
		`{"sources":["codex-desktop","codex-cli","claude-code","other"]}`,
	} {
		if _, err := configuredSources(sourceConfigFixture(t, text)); err == nil {
			t.Fatalf("admitted %s", text)
		}
	}
	if hasConfiguredSource([]string{claudeCodeSource}, codexDesktopSource) {
		t.Fatal("a Claude-only installation acquired Desktop")
	}
}

func TestConfiguredNativeEndpointsRemainExplicitAndPrivate(t *testing.T) {
	if got, err := configuredDaemonSocket(map[string]any{}); err != nil || got != "" {
		t.Fatalf("absent daemon path = %q, %v", got, err)
	}
	for _, value := range []any{"", "relative.sock", "ws://127.0.0.1:1234", 1, "x\x00y"} {
		if _, err := configuredDaemonSocket(map[string]any{"codexDaemonSocket": value}); err == nil {
			t.Fatalf("admitted daemon socket %v", value)
		}
	}
	path := filepath.Join(filepath.VolumeName(t.TempDir())+string(filepath.Separator), "filo-fixture.sock")
	if got, err := configuredDaemonSocket(map[string]any{"codexDaemonSocket": path}); err != nil || got != path {
		t.Fatalf("explicit socket = %q, %v", got, err)
	}
	if got, err := configuredClaudeBridge(map[string]any{}); err != nil || got != "127.0.0.1:7438" {
		t.Fatalf("default Mod endpoint = %q, %v", got, err)
	}
	if got, err := configuredClaudeBridge(map[string]any{"claudeBridgePort": float64(45678)}); err != nil || got != "127.0.0.1:45678" {
		t.Fatalf("explicit Mod endpoint = %q, %v", got, err)
	}
	for _, value := range []any{nil, "7437", float64(7437), float64(0), float64(1023), float64(65536), 7437.5} {
		if _, err := configuredClaudeBridge(map[string]any{"claudeBridgePort": value}); err == nil {
			t.Fatalf("admitted Mod endpoint %v", value)
		}
	}
}
