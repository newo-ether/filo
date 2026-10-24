//go:build !windows

package claude

import "syscall"

func nativeProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
