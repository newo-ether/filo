//go:build windows

// This fixture passes entry validation, then fails after SCM registration.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) == 1 {
		fmt.Fprintln(os.Stderr, "Usage: filo <qualification>")
		os.Exit(2)
	}
	os.Exit(3)
}
