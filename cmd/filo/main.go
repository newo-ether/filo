// Command filo is the single Filo entry point: the same binary runs the desktop
// IPC gateway, the standalone preview gateway and the private task executor
// runner, so a deployment installs one artifact and needs no Node.js runtime.
package main

import (
	"os"

	"github.com/newo-ether/filo/internal/service"
)

func main() {
	os.Exit(service.Main(os.Args[1:], os.Stderr))
}
