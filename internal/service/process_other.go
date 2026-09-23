//go:build !windows

package service

import (
	"errors"
	"os"
	"syscall"
)

// processAlive mirrors `process.kill(pid, 0)`: no error means the process
// exists, and a permission failure still proves it exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// detachedProcessAttributes mirrors the TS `detached: true` spawn, which starts
// the runner in its own session so it survives the leasing helper.
func detachedProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
