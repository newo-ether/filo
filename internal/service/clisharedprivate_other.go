//go:build !windows

package service

import "os"

func prepareCLISharedDirectory(directory string) error {
	return os.MkdirAll(directory, 0700)
}
