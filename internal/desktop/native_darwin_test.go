package desktop_test

import (
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/windowsapp"
)

// Only initializes the protocol; never reads conversations or performs mutations.
func TestDarwinNativeInitialize(t *testing.T) {
	if os.Getenv("FILO_MACOS_READ_PROBE") != "1" {
		t.Skip("Native read-only probe is disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path, err := desktop.DefaultEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	client := desktop.NewClient(desktop.Options{Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
		return desktop.DialPipe(ctx, path, func(ctx context.Context, pid uint32) error {
			app, err := windowsapp.Inspect(ctx, pid, strconv.Itoa(os.Geteuid()))
			if err == nil {
				t.Logf("Verified PID=%d bundle=%s executable=%s", pid, app.PackageFullName, app.Executable)
			}
			return err
		})
	}})
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
}
