package windowsapp

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDarwinRejectUntrustedProcess(t *testing.T) {
	uid := strconv.Itoa(os.Geteuid())
	if _, err := Inspect(context.Background(), uint32(os.Getpid()), uid); err == nil {
		t.Fatal("The test process must not be accepted as Codex")
	}
	if _, err := NativeHome(context.Background(), "0"); err == nil {
		t.Fatal("Accepted root")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NativeHome(ctx, uid); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestDarwinNativeHomeUsesCodexDirectory(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home, err := NativeHome(context.Background(), account.Uid)
	if err != nil || home != filepath.Join(account.HomeDir, ".codex") {
		t.Fatalf("%q %v", home, err)
	}
}
