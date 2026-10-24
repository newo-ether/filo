package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// cliHelpProof caches the app-server help check per executable file identity.
// Status runs on every /v1/info; spawning the native help each time made one
// configured CLI source cost about a second per request. A replaced or updated
// executable changes size or modification time and is verified again.
type cliHelpProof struct {
	size    int64
	modTime time.Time
	ok      bool
}

var cliHelpProofs = struct {
	sync.Mutex
	entries map[string]cliHelpProof
}{entries: make(map[string]cliHelpProof)}

// runCLIRuntimeHelp is replaceable in tests.
var runCLIRuntimeHelp = func(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "app-server", "--help")
	command.Dir = filepath.Dir(path)
	command.Env = nativeProcessEnvironment(path)
	command.SysProcAttr = detachedProcessAttributes()
	return command.Output()
}

// resolveCLIRuntime discovers the selected user's native CLI, never a Desktop
// bundle or a shell shim. Its harmless native help must prove the private
// authenticated app-server transport before the executable can be used.
func resolveCLIRuntime() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidates := []string{
		filepath.Join(home, ".codex", "packages", "standalone", "current", "bin", name),
		filepath.Join(home, ".local", "bin", name),
	}
	if matches, err := filepath.Glob(filepath.Join(home, "AppData", "Local", "OpenAI", "Codex", "bin", "*", name)); err == nil {
		candidates = append(candidates, matches...)
	}
	// npm's shim is never executed. Its official platform package may contain a
	// native binary with the same protocol, which can be verified directly.
	for _, root := range []string{
		filepath.Join(home, "AppData", "Roaming", "npm", "node_modules", "@openai"),
		filepath.Join(home, ".npm-global", "lib", "node_modules", "@openai"),
	} {
		for _, pattern := range []string{"codex/vendor/*/codex/" + name, "codex-*/vendor/*/codex/" + name,
			"codex/node_modules/@openai/codex-*/vendor/*/codex/" + name} {
			matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
			candidates = append(candidates, matches...)
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		candidates = append(candidates, path)
	}
	for _, candidate := range candidates {
		path, err := verifyCLIRuntime(candidate)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("Verified native Codex CLI runtime is unavailable")
}

func verifyCLIRuntime(candidate string) (string, error) {
	if !filepath.IsAbs(candidate) || !strings.EqualFold(filepath.Base(candidate), "codex"+cliExecutableSuffix()) {
		return "", errors.New("Invalid native Codex CLI executable")
	}
	path, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	clean := strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(clean, "/windowsapps/") || strings.Contains(clean, "/resources/") {
		return "", errors.New("Desktop runtime cannot supply CLI execution")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("Native Codex CLI executable is unavailable")
	}
	cliHelpProofs.Lock()
	proof, cached := cliHelpProofs.entries[path]
	cliHelpProofs.Unlock()
	if !cached || proof.size != info.Size() || !proof.modTime.Equal(info.ModTime()) {
		output, err := runCLIRuntimeHelp(path)
		proof = cliHelpProof{size: info.Size(), modTime: info.ModTime(),
			ok: err == nil && len(output) <= 64*1024 && cliRuntimeHelpSupported(string(output))}
		if err == nil || proof.ok {
			// A timeout or launch failure is transient and is not remembered.
			cliHelpProofs.Lock()
			cliHelpProofs.entries[path] = proof
			cliHelpProofs.Unlock()
		}
	}
	if !proof.ok {
		return "", errors.New("Native CLI lacks the authenticated app-server transport")
	}
	return path, nil
}

func cliExecutableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func cliRuntimeHelpSupported(help string) bool {
	for _, flag := range []string{"app-server", "--listen", "--ws-auth", "capability-token", "--ws-token-file"} {
		if !strings.Contains(help, flag) {
			return false
		}
	}
	return true
}
