//go:build windows

package service

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type guardianHandle struct{ handle windows.Handle }

func (p guardianHandle) alive() bool {
	status, err := windows.WaitForSingleObject(p.handle, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
func (p guardianHandle) release() error { return windows.TerminateProcess(p.handle, 0) }
func (p guardianHandle) close()         { _ = windows.CloseHandle(p.handle) }

func openGuardianProcess(pid int, native bool) (guardianProcess, error) {
	access := uint32(windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION)
	if native {
		access |= windows.PROCESS_TERMINATE
	}
	handle, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	if native {
		var path [32768]uint16
		size := uint32(len(path))
		err := windows.QueryFullProcessImageName(handle, 0, &path[0], &size)
		if err != nil || !strings.EqualFold(filepath.Base(windows.UTF16ToString(path[:size])), "codex.exe") {
			_ = windows.CloseHandle(handle)
			return nil, errGuardianState
		}
	}
	return guardianHandle{handle}, nil
}

// RunNativeGuardian watches one Filo-owned native child independently of its keeper.
func RunNativeGuardian(directory string) error { return runGuardian(directory, openGuardianProcess) }
