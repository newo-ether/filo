//go:build windows

package claude

import "syscall"

func nativeProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000200, HideWindow: true}
}
