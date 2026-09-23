//go:build !windows

package service

import (
	"errors"
	"time"
)

// windowsPowerShell is unavailable outside Windows, matching the TS platform
// gate that refuses every call on a non-Windows host.
func windowsPowerShell(script string, args []string, timeout time.Duration) (string, error) {
	return "", errors.New("An absolute script and Windows environment are required")
}
