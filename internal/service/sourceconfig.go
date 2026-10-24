package service

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	codexDesktopSource  = "codex-desktop"
	codexCLISource      = "codex-cli"
	claudeCodeSource    = "claude-code"
	claudeDesktopSource = "claude-desktop"
)

// configuredSources keeps the original installation Desktop-only unless its
// private configuration explicitly opts into additional native access paths.
func configuredSources(config map[string]any) ([]string, error) {
	raw, present := config["sources"]
	if !present {
		return []string{codexDesktopSource}, nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) < 1 || len(values) > 4 {
		return nil, errors.New("Expected one to four configured Filo sources")
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if !ok || seen[id] || (id != codexDesktopSource && id != codexCLISource && id != claudeCodeSource && id != claudeDesktopSource) {
			return nil, errors.New("Invalid or duplicate configured Filo source")
		}
		seen[id] = true
		result = append(result, id)
	}
	return result, nil
}

func hasConfiguredSource(sources []string, id string) bool {
	for _, source := range sources {
		if source == id {
			return true
		}
	}
	return false
}

// configuredDaemonSocket accepts only an explicit bounded local socket path.
// Absence leaves discovery to the original user's daemon adapter; it never
// authorizes starting a daemon or falling back to a separate app-server.
func configuredDaemonSocket(config map[string]any) (string, error) {
	raw, present := config["codexDaemonSocket"]
	if !present {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok || value == "" || !filepath.IsAbs(value) || strings.ContainsRune(value, 0) || len([]byte(value)) >= 108 {
		return "", errors.New("Invalid configured Codex daemon socket")
	}
	return value, nil
}

// configuredClaudeBridge keeps the opt-in Mod endpoint private and separate
// from the public encrypted gateway and its bearer credential.
func configuredClaudeBridge(config map[string]any) (string, error) {
	raw, present := config["claudeBridgePort"]
	if !present {
		return "127.0.0.1:7438", nil
	}
	_, port, valid := readEntryBinding(map[string]any{"bind": "127.0.0.1", "port": raw})
	if !valid || port == 7437 {
		return "", errors.New("Invalid configured Claude Mod bridge port")
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
}

func validateSourceEndpoints(config map[string]any, sources []string, workerPort int) error {
	if hasConfiguredSource(sources, codexCLISource) {
		if _, err := configuredDaemonSocket(config); err != nil {
			return err
		}
	}
	if hasConfiguredSource(sources, claudeCodeSource) || hasConfiguredSource(sources, claudeDesktopSource) {
		address, err := configuredClaudeBridge(config)
		if err != nil {
			return err
		}
		_, rawPort, _ := net.SplitHostPort(address)
		if rawPort == strconv.Itoa(workerPort) {
			return errors.New("Claude Mod bridge must use a separate loopback port")
		}
	}
	return nil
}

// sourceReadScope is request-local read projection. Every adapter must compare
// its fresh scope after the page read; it is never used at mutation dispatch.
type sourceReadScope struct{}

func withSourceReadScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, sourceReadScope{}, scope)
}

func cachedSourceReadScope(ctx context.Context) string {
	value, _ := ctx.Value(sourceReadScope{}).(string)
	return value
}
