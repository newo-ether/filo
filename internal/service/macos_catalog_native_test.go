//go:build darwin

package service

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/history"
)

func TestMacOSNativeCatalogRead(t *testing.T) {
	if os.Getenv("FILO_MACOS_CATALOG_PROBE") != "1" {
		t.Skip("Native read-only catalog probe is disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	page, err := history.NewNativeHistory(nativeHome(strconv.Itoa(os.Geteuid()))).List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Read-only catalog rows=%d elapsed=%s", len(page.Sessions), time.Since(start))
}
