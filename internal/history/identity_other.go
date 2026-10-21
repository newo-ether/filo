//go:build !windows

package history

import "os"

func openIdentityFile(path string) (*os.File, error) {
	return os.Open(path)
}
