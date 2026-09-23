package service

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// State writers replace complete files atomically. Readers must permit that
// replacement, and tolerate only Windows' short sharing/lock transition.
func openExecutorFile(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		handle, err := syscall.CreateFile(name, syscall.GENERIC_READ,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
			nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return os.NewFile(uintptr(handle), path), nil
		}
		if (!errors.Is(err, syscall.Errno(32)) && !errors.Is(err, syscall.Errno(33))) ||
			!time.Now().Before(deadline) {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Windows can briefly deny replacement while another reader owns the old file.
// Retry only publishing this same complete temporary file; never rewrite the
// destination in place or suppress a persistent permissions failure.
func renameExecutorFile(source, target string) error {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		err := os.Rename(source, target)
		if err == nil {
			return nil
		}
		if (!errors.Is(err, syscall.Errno(5)) && !errors.Is(err, syscall.Errno(32)) &&
			!errors.Is(err, syscall.Errno(33))) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
