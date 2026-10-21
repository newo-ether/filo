//go:build windows

package codex

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func daemonFixtureDirectory(t *testing.T) (string, string, string) {
	t.Helper()
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if err := requireDaemonNonElevated(token); err != nil {
		t.Skip("UDS fixture requires an ordinary selected-user token: ", err)
	}
	root, err := os.MkdirTemp("", "fuds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "private")
	name, err := windows.UTF16PtrFromString(private)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(name, &attributes); err != nil {
		t.Fatal(err)
	}
	return private, root, sid
}
