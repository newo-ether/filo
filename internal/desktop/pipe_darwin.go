package desktop

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// DefaultPipePath is relative to the user home on Darwin; callers use DefaultEndpoint.
const DefaultPipePath = ".codex/ipc/ipc.sock"

func DefaultEndpoint() (string, error) {
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return "", fmt.Errorf("FILO_IPC_HOME: %w", err)
	}
	return filepath.Join(account.HomeDir, DefaultPipePath), nil
}

// DialPipe verifies endpoint permissions and kernel peer identity before sending protocol data.
func DialPipe(ctx context.Context, path string, verify VerifyPipePeer) (net.Conn, error) {
	if verify == nil || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("FILO_IPC_ENDPOINT: absolute socket and peer verifier required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("FILO_IPC_STAT: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("FILO_IPC_OWNER: socket must belong to current user with mode 0600")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("FILO_IPC_DIAL: %w", err)
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = connection.Close()
		}
	}()
	raw, err := connection.(*net.UnixConn).SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("FILO_IPC_HANDLE: %w", err)
	}
	var pid int
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			peerErr = e
			return
		}
		if cred.Uid != uint32(os.Geteuid()) {
			peerErr = fmt.Errorf("peer UID differs")
			return
		}
		pid, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err != nil {
		return nil, fmt.Errorf("FILO_IPC_PEER: %w", err)
	}
	if peerErr != nil {
		return nil, fmt.Errorf("FILO_IPC_PEER: %w", peerErr)
	}
	if pid <= 0 {
		return nil, fmt.Errorf("FILO_IPC_PID: invalid peer PID")
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	err = verify(ctx, uint32(pid))
	stop()
	if err != nil {
		return nil, fmt.Errorf("FILO_IPC_APPLICATION: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted = true
	return connection, nil
}
