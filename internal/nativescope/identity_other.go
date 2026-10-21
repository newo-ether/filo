//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package nativescope

import "os"

func openAuthFile(string) (*os.File, error) {
	return nil, ErrUnavailable
}

func fileIdentity(*os.File) ([]byte, error) {
	return nil, ErrUnavailable
}
