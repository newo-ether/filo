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
// Installed artifacts resolve from the running executable's deployment root.
// FILO_DEPLOYMENT_ROOT overrides that anchor with an absolute path, allowing
// tests to use a private layout instead of the test binary's build-cache path.
func deploymentPath(segments ...string) (string, error) {
	root, err := deploymentRoot()
	if err != nil {
		return "", err
	}
	if len(segments) > 0 && segments[0] == "tools" && filepath.Base(rootForExecutablePath(root)) == "bin" {
		segments = append([]string{"bin"}, segments[1:]...)
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

// Installed binaries live in bin; source bundles can still use tools.
func rootForExecutablePath(root string) string {
	if executable, err := os.Executable(); err == nil && rootForExecutable(executable) == root {
		return filepath.Dir(executable)
	}
	return ""
}

func rootForExecutable(executable string) string {
	directory := filepath.Dir(executable)
	if filepath.Base(directory) == "tools" || filepath.Base(directory) == "bin" {
		return filepath.Dir(directory)
	}
	return directory
}
