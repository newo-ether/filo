//go:build windows

package windowsapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestManifestExecutableAndExtendedPaths(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "app", "ChatGPT.exe")
	if err := os.Mkdir(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("isolated fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := `<Package><Identity Name="OpenAI.Codex"/><Applications><Application Executable="app\ChatGPT.exe"/></Applications></Package>`
	if err := os.WriteFile(filepath.Join(root, "AppxManifest.xml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{exe, strings.ToUpper(exe), `\\?\` + exe} {
		if err := validateExecutable(root, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(root, "other.exe"), filepath.Join(root, "app", "Child.exe"), `\\server\share\ChatGPT.exe`, "relative.exe"} {
		if err := validateExecutable(root, path); err == nil {
			t.Fatal("Unregistered executable accepted", path)
		}
	}
}

func TestMalformedManifestCannotAuthorizeExecutable(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "ChatGPT.exe")
	if err := os.WriteFile(exe, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`<Package><Identity Name="Other"/><Applications><Application Executable="ChatGPT.exe"/></Applications></Package>`,
		`<Package><Identity Name="OpenAI.Codex"/><Applications><Application Executable="..\ChatGPT.exe"/></Applications></Package>`,
		`<Package`, strings.Repeat("x", maxManifestBytes+1),
	} {
		if err := os.WriteFile(filepath.Join(root, "AppxManifest.xml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateExecutable(root, exe); err == nil {
			t.Fatal("Malformed manifest accepted")
		}
	}
}

func TestWindowsStringBoundaries(t *testing.T) {
	for _, length := range []uint32{0, 1, 32769} {
		if _, err := boundedString(func(size *uint32, _ *uint16) uintptr { *size = length; return 0 }); err == nil {
			t.Fatal("Invalid length accepted", length)
		}
	}
	if _, err := boundedString(func(size *uint32, buffer *uint16) uintptr { *size = 2; unsafe.Slice(buffer, 2)[1] = 'x'; return 0 }); err == nil {
		t.Fatal("Missing terminator accepted")
	}
	if _, err := boundedString(func(*uint32, *uint16) uintptr { return uintptr(windows.ERROR_ACCESS_DENIED) }); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal(err)
	}
}

func TestUnpackagedProcessDoesNotBecomeOriginalDesktop(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), uint32(os.Getpid()), user.User.Sid.String()); err == nil {
		t.Fatal("Test runner accepted as original Desktop")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, 1, "S-1-5-21-1-2-3-1001"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTokenQueryUsesSelectedIdentityOnSuccessAndError(t *testing.T) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("Isolated registration failure")
	for _, expectedErr := range []error{nil, failure} {
		value, err := asToken(context.Background(), token, func() (string, error) {
			var thread windows.Token
			if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &thread); err != nil {
				return "", err
			}
			defer thread.Close()
			actual, err := thread.GetTokenUser()
			if err != nil {
				return "", err
			}
			return actual.User.Sid.String(), expectedErr
		})
		if value != user.User.Sid.String() || !errors.Is(err, expectedErr) {
			t.Fatalf("Token result %q %v", value, err)
		}
	}
}

func TestTokenThreadRestoresBeforeReturning(t *testing.T) {
	var source, token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := windows.DuplicateTokenEx(source, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, failure := range []error{nil, errors.New("Read failed")} {
		result := onTokenThread(token, func() (string, error) { return "checked", failure })
		if result.value != "checked" || !errors.Is(result.err, failure) {
			t.Fatal(result)
		}
		var remaining windows.Token
		err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &remaining)
		if remaining != 0 {
			_ = remaining.Close()
		}
		if !errors.Is(err, windows.ERROR_NO_TOKEN) {
			t.Fatalf("Thread kept impersonation after return: %v", err)
		}
	}
}

func TestCancelledCallerLeavesOnlyItsIndependentReadToFinish(t *testing.T) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		_, err := asToken(ctx, token, func() (string, error) { close(entered); <-release; close(finished); return "done", nil })
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Token query did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Query caller remained blocked")
	}
	select {
	case <-finished:
		t.Fatal("Cancellation ended the independent query")
	default:
	}
}
