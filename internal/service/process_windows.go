//go:build windows

package service

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// processAlive requires a running process, since an exited Windows process can
// remain openable while another process retains its handle. Access denial and
// unreadable exit status stay conservative and cannot authorize a new writer.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == 259 // STILL_ACTIVE is not exposed by this x/sys version.
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
