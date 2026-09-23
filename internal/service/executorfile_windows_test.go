package service

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestExecutorReadWaitsForShortWindowsFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{\"pid\":42}"), 0600); err != nil {
		t.Fatal(err)
	}
	name, _ := syscall.UTF16PtrFromString(path)
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() { time.Sleep(50 * time.Millisecond); syscall.CloseHandle(handle); close(released) }()
	value, err := ReadExecutorFile(filepath.Dir(path), filepath.Base(path))
	<-released
	if err != nil || value.(map[string]any)["pid"] != float64(42) {
		t.Fatalf("read: %v %v", value, err)
	}
}

func TestExecutorAtomicReplacementWaitsForReaderAndMissingStaysError(t *testing.T) {
	directory := t.TempDir()
	if err := SaveExecutorFile(directory, "state.json", map[string]any{"pid": 1}); err != nil {
		t.Fatal(err)
	}
	reader, err := openExecutorFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	released := make(chan struct{})
	go func() { time.Sleep(50 * time.Millisecond); reader.Close(); close(released) }()
	if err := SaveExecutorFile(directory, "state.json", map[string]any{"pid": 2}); err != nil {
		t.Fatal(err)
	}
	<-released
	value, err := ReadExecutorFile(directory, "state.json")
	if err != nil || value.(map[string]any)["pid"] != float64(2) {
		t.Fatalf("replacement: %v %v", value, err)
	}
	_, err = openExecutorFile(filepath.Join(directory, "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing record: %v", err)
	}
}
