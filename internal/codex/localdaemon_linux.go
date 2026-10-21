//go:build linux

package codex

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateDaemonSocket(path, userID string) (string, func(), error) {
	if userID != strconv.Itoa(os.Geteuid()) || os.Geteuid() == 0 {
		return "", nil, errors.New("Codex daemon requires the selected ordinary user")
	}
	directory := filepath.Dir(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(directory))
	if err != nil {
		return "", nil, err
	}
	directory = filepath.Join(parent, filepath.Base(directory))
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return "", nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0777 != 0700 {
		unix.Close(fd)
		return "", nil, errors.New("Codex daemon socket directory is not private to selected user")
	}
	return filepath.Join(directory, filepath.Base(path)), func() { _ = unix.Close(fd) }, nil
}

func verifyDaemonPeer(connection net.Conn, userID string) error {
	if userID != strconv.Itoa(os.Geteuid()) || os.Geteuid() == 0 {
		return errors.New("Codex daemon requires the selected ordinary user")
	}
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return errors.New("Codex daemon peer is not a local socket")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	var credential *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		credential, peerErr = unix.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if peerErr != nil || credential == nil || credential.Uid != uint32(os.Geteuid()) {
		return errors.New("Codex daemon peer account mismatch")
	}
	return nil
}
