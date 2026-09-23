//go:build windows

// This console-subsystem fixture is intentionally launched without hide flags
// of its own, so its parent must prevent console allocation.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	if err := os.WriteFile(filepath.Join(os.Args[2], "console.txt"), []byte(fmt.Sprint(console)), 0600); err != nil {
		os.Exit(1)
	}
	fmt.Println("stdout drained")
	fmt.Fprintln(os.Stderr, "stderr drained")
}
