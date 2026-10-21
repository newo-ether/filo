//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package nativescope

import (
	"encoding/binary"
	"os"
	"syscall"
)

func openAuthFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	return os.NewFile(uintptr(fd), path), nil
}

func fileIdentity(file *os.File) ([]byte, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return nil, ErrUnavailable
	}
	identity := make([]byte, 16)
	binary.BigEndian.PutUint64(identity[:8], uint64(stat.Dev))
	binary.BigEndian.PutUint64(identity[8:], uint64(stat.Ino))
	return identity, nil
}
