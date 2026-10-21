//go:build windows

package codex

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Read-only validation follows the official daemon's protected user-only
// rendezvous contract. The directory handle pins it until connection setup ends.
func validateDaemonSocket(path, userID string) (string, func(), error) {
	user, err := localDaemonUser(userID)
	if err != nil {
		return "", nil, err
	}
	directory := filepath.Dir(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(directory))
	if err != nil {
		return "", nil, errors.New("Codex daemon socket directory is unavailable")
	}
	directory = filepath.Join(parent, filepath.Base(directory))
	name, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return "", nil, err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", nil, errors.New("Codex daemon socket directory cannot be verified")
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = windows.CloseHandle(handle)
		}
	}()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", nil, errors.New("Codex daemon socket directory is untrusted")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return "", nil, errors.New("Codex daemon socket owner mismatch")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return "", nil, errors.New("Codex daemon socket directory ACL is not protected")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		return "", nil, errors.New("Codex daemon socket directory is not private")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return "", nil, err
	}
	const fileAllAccess = 0x001f01ff
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Header.AceFlags != windows.CONTAINER_INHERIT_ACE|windows.OBJECT_INHERIT_ACE ||
		ace.Mask != fileAllAccess || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user.User.Sid) {
		return "", nil, errors.New("Codex daemon socket directory is not private to selected user")
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		return "", nil, errors.New("Codex daemon canonical socket path is unavailable")
	}
	resolved := windows.UTF16ToString(buffer[:n])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		return "", nil, errors.New("Codex daemon requires a local socket")
	}
	resolved = strings.TrimPrefix(resolved, `\\?\`)
	accepted = true
	return filepath.Join(resolved, filepath.Base(path)), func() { _ = windows.CloseHandle(handle) }, nil
}

func localDaemonUser(userID string) (*windows.Tokenuser, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	if user.User.Sid.String() != userID {
		return nil, errors.New("Codex daemon requires the selected user process")
	}
	if err := requireDaemonNonElevated(token); err != nil {
		return nil, err
	}
	return user, nil
}

func requireDaemonNonElevated(token windows.Token) error {
	var elevated, returned uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &returned); err != nil {
		return err
	}
	if returned != 4 || elevated != 0 {
		return errors.New("Codex daemon requires non-elevated user tokens")
	}
	return nil
}

func verifyDaemonPeer(connection net.Conn, userID string) error {
	if _, err := localDaemonUser(userID); err != nil {
		return err
	}
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return errors.New("Codex daemon peer is not a local socket")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	var pid, returned uint32
	var peerErr error
	// afunix.h: _WSAIOR(IOC_VENDOR, 256).
	const sioUnixGetPeerPID = 0x58000100
	if err := raw.Control(func(fd uintptr) {
		peerErr = windows.WSAIoctl(windows.Handle(fd), sioUnixGetPeerPID, nil, 0,
			(*byte)(unsafe.Pointer(&pid)), 4, &returned, nil, 0)
	}); err != nil {
		return err
	}
	if peerErr != nil || pid == 0 {
		return errors.New("Codex daemon kernel peer identity is unavailable")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.String() != userID {
		return errors.New("Codex daemon peer account mismatch")
	}
	return requireDaemonNonElevated(token)
}
