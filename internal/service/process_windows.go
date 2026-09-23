//go:build windows

package service

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// processAlive mirrors `process.kill(pid, 0)`: a live pid is proven by an open
// process handle, and access denial still proves the process exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	_ = windows.CloseHandle(handle)
	return true
}

// detachedProcessAttributes keeps the child in an independent process group
// while explicitly forbidding Windows from creating or inheriting a console.
// DETACHED_PROCESS cannot be combined with CREATE_NO_WINDOW, and permits a
// console application to allocate a console later, so it is not used here.
func detachedProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}
