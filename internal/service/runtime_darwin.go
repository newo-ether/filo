package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/windowsapp"
)

func resolveDesktopRuntime() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemServiceDesktopTimeout)
	defer cancel()
	path, err := desktop.DefaultEndpoint()
	if err != nil {
		return "", err
	}
	var app windowsapp.Application
	conn, err := desktop.DialPipe(ctx, path, func(ctx context.Context, pid uint32) error {
		var err error
		app, err = windowsapp.Inspect(ctx, pid, strconv.Itoa(os.Geteuid()))
		return err
	})
	if err != nil {
		return "", err
	}
	conn.Close()
	executable := filepath.Join(app.PackageRoot, "Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex")
	requirement := `=anchor apple generic and certificate leaf[subject.OU] = "2DC432GLL2"`
	if output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "-R", requirement, executable).CombinedOutput(); err != nil {
		return "", fmt.Errorf("FILO_RUNTIME_SIGNATURE: %w: %s", err, output)
	}
	return executable, nil
}
