//go:build !windows

package service

import (
	"errors"
	"time"
)

// windowsPowerShell refuses calls on non-Windows hosts.
func windowsPowerShell(script string, args []string, timeout time.Duration) (string, error) {
	return "", errors.New("An absolute script and Windows environment are required")
}
