//go:build windows

package nativescope

import (
	"encoding/binary"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openAuthFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	return os.NewFile(uintptr(handle), path), nil
}

func fileIdentity(file *os.File) ([]byte, error) {
	// FILE_ID_INFO is the volume serial followed by the filesystem's 128-bit ID.
	// Query the opened read-only handle so a path string cannot establish identity.
	var info struct {
		Volume uint64
		ID     [16]byte
	}
	err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil || info.ID == [16]byte{} {
		return nil, ErrUnavailable
	}
	identity := make([]byte, 24)
	binary.BigEndian.PutUint64(identity[:8], info.Volume)
	copy(identity[8:], info.ID[:])
	return identity, nil
}
