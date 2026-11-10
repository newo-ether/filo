//go:build !darwin

package service

func resolveDesktopRuntime() (string, error) {
	script, err := deploymentPath("scripts", executorDesktopRuntimeScript)
	if err != nil {
		return "", err
	}
	stdout, err := windowsPowerShell(script, nil, executorDesktopRuntimeTimeout)
	if err != nil {
		return "", err
	}
	return parseDesktopRuntime(stdout)
}
