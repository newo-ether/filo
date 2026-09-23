//go:build windows

// The scheduler entry is built with -H=windowsgui. Its PowerShell child is also
// created without a console, before Windows Terminal can claim any process.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/newo-ether/filo/internal/service"
	"golang.org/x/sys/windows"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) != 4 || args[0] != "-Role" || args[2] != "-StateDirectory" ||
		(args[1] != "Host" && args[1] != "Gateway" && args[1] != "TaskGuardian") {
		return 2
	}
	state, err := filepath.Abs(args[3])
	if err != nil {
		return 1
	}
	logs := filepath.Join(state, "logs")
	if os.MkdirAll(logs, 0700) != nil {
		return 1
	}
	log, err := os.OpenFile(filepath.Join(logs, args[1]+".launcher.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return 1
	}
	defer log.Close()
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	_, _ = fmt.Fprintf(log, "console=%d; pid=%d\n", console, os.Getpid())
	if console != 0 {
		return 1
	}
	if args[1] == "TaskGuardian" {
		if err := service.RunNativeGuardian(state); err != nil {
			_ = os.WriteFile(filepath.Join(state, "guardian.error"), []byte("Native lifetime protection failed"), 0600)
			return 1
		}
		return 0
	}
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	runner := filepath.Join(filepath.Dir(self), "..", "scripts", "windows", "Run-Standalone.ps1")
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	command := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-File", runner,
		"-Role", args[1], "-StateDirectory", state)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	command.Stdout, command.Stderr = log, log
	if err := command.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		_, _ = fmt.Fprintln(log, "Filo background runner could not start")
		return 1
	}
	return 0
}
