package service

import (
	"errors"
	"os"
	"path/filepath"
)

// deploymentRootEnvironment overrides the root the installed layout lives in.
const deploymentRootEnvironment = "FILO_DEPLOYMENT_ROOT"

// deploymentPath resolves one artifact of the installed layout.
//
// The TypeScript source walks up from its own file (`new URL('../../../../tools/
// ..., import.meta.url)`), which a compiled Go binary cannot do. The equivalent
// anchor is the directory of the running executable, so a deployment that ships
// `tools/FiloBackground.exe` and `scripts/resolve-desktop-runtime.ps1` next to
// the service binary resolves both without configuration. An absolute
// FILO_DEPLOYMENT_ROOT overrides that anchor, which also gives tests a private
// layout instead of the Go build cache the test binary lives in.
func deploymentPath(segments ...string) (string, error) {
	root, err := deploymentRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, segments...)...), nil
}

// deploymentRoot returns the directory the installed layout is rooted at.
func deploymentRoot() (string, error) {
	override := os.Getenv(deploymentRootEnvironment)
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("The deployment root must be absolute")
		}
		return override, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return rootForExecutable(executable), nil
}

// Installed binaries live in tools; scripts and the guardian are siblings of tools.
func rootForExecutable(executable string) string {
	directory := filepath.Dir(executable)
	if filepath.Base(directory) == "tools" {
		return filepath.Dir(directory)
	}
	return directory
}
