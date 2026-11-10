package service

import (
	"fmt"
	"net/url"
	"strconv"
	"syscall"
)

// runNativeHost starts only a private Filo helper; it never restarts or controls Desktop.
func runNativeHost(arguments []string) error {
	if len(arguments) != 1 {
		return fmt.Errorf("FILO_HOST_ARGUMENT: expected loopback websocket URL")
	}
	endpoint, err := url.Parse(arguments[0])
	if err != nil {
		return fmt.Errorf("FILO_HOST_URL: %w", err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || endpoint.Scheme != "ws" || endpoint.Hostname() != "127.0.0.1" || port < 1024 || port > 65535 || endpoint.Path != "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("FILO_HOST_URL: expected loopback websocket URL")
	}
	executable, err := resolveDesktopRuntime()
	if err != nil {
		return err
	}
	return syscall.Exec(executable, []string{executable, "app-server", "--listen", arguments[0]}, nativeProcessEnvironment(executable))
}
