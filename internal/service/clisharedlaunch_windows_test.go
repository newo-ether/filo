//go:build windows

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSharedCLIWindowsPrivateRegistryAndStartupFailureCleanup(t *testing.T) {
	for _, liveLease := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired lease", true: "missing runtime"}[liveLease], func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "registry", "owner")
			if err := prepareCLISharedDirectory(directory); err != nil {
				t.Fatal(err)
			}
			security, err := windows.GetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil || !strings.Contains(security.String(), "D:P") {
				t.Fatalf("private registry DACL is not protected: %v", err)
			}
			token := strings.Repeat("b", 64)
			lease := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if liveLease && r.Header.Get("Authorization") == "Bearer "+token {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				http.NotFound(w, r)
			}))
			defer lease.Close()
			config := cliSharedOwnerConfig{Runtime: filepath.Join(directory, "missing.exe"), Workspace: directory,
				Token: token, Lease: lease.URL + "/lease"}
			if err := SaveExecutorFile(directory, cliSharedOwnerConfigName, config); err != nil {
				t.Fatal(err)
			}
			if err := RunCLISharedOwner(directory); err == nil {
				t.Fatal("invalid startup succeeded")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("failed startup retained private registry and credentials")
			}
			if cliSharedLease(context.Background(), cliSharedRecord{Lease: "http://example.invalid/lease", Token: token}) {
				t.Fatal("non-loopback lease admitted")
			}
		})
	}
}
