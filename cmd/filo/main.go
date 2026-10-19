// Command filo runs the Desktop IPC gateway, standalone gateway and private
// task executor from one executable.
package main

import (
	"os"

	"github.com/newo-ether/filo/internal/service"
)

func main() {
	os.Exit(service.Main(os.Args[1:], os.Stderr))
}
