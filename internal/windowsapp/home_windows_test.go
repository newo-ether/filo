//go:build windows

package windowsapp

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

func TestSelectedHomeExpandsOnlySelectedAccountVariables(t *testing.T) {
	t.Setenv("USERPROFILE", `Z:\WrongUser`)
	t.Setenv("CODEX_HOME", `Z:\WrongNativeHome`)
	profile := `D:\Profiles\Selected User`
	for _, sample := range []struct{ custom, want string }{
		{"", profile + `\.codex`},
		{`%userprofile%\native`, profile + `\native`},
		{`%HOMEDRIVE%%HOMEPATH%\native`, profile + `\native`},
		{`%LOCALAPPDATA%\native`, profile + `\AppData\Local\native`},
		{`%APPDATA%\native`, profile + `\AppData\Roaming\native`},
		{`%SYSTEMROOT%\native`, `C:\Windows\native`},
		{`%PROGRAMDATA%\native`, `C:\ProgramData\native`},
		{`E:\Custom\..\Native`, `E:\Native`},
		{`\\?\E:\Native`, `\\?\E:\Native`},
		{`\\server\share\native`, `\\server\share\native`},
	} {
		got, err := resolveHome(profile, sample.custom, `C:\Windows`, `C:\ProgramData`)
		if err != nil || got != sample.want {
			t.Fatalf("%q: %q != %q: %v", sample.custom, got, sample.want, err)
		}
	}
}

func TestSelectedHomeRejectsUnknownVariablesAndUnrootedPaths(t *testing.T) {
	for _, custom := range []string{`%PATH%\native`, `relative`, `D:relative`, `\relative`, "D:\\bad\x00path", `D:\` + strings.Repeat("x", 32768)} {
		if _, err := resolveHome(`D:\Profile`, custom, `C:\Windows`, `C:\ProgramData`); err == nil {
			t.Fatal("Invalid native home accepted", len(custom))
		}
	}
	if _, err := resolveHome("relative", "", `C:\Windows`, `C:\ProgramData`); err == nil {
		t.Fatal("Relative profile accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NativeHome(ctx, "ignored"); err != context.Canceled {
		t.Fatal(err)
	}
	for _, sid := range []string{"", `S-1-5-18`, `S-1-5-21-1\Environment`} {
		if _, err := NativeHome(context.Background(), sid); err == nil {
			t.Fatal("Nonordinary account accepted")
		}
	}
}

type registryFixture struct {
	data []byte
	kind uint32
	err  error
}

func (value registryFixture) GetValue(_ string, buffer []byte) (int, uint32, error) {
	copy(buffer, value.data)
	return len(value.data), value.kind, value.err
}

func TestRegistryPathReadIsBoundedAndRequiresStringData(t *testing.T) {
	units := utf16.Encode([]rune("D:\\Profile 雪\x00"))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	got, _, err := registryString(registryFixture{data, registry.SZ, nil}, "path")
	if err != nil || got != "D:\\Profile 雪" {
		t.Fatal(got, err)
	}
	for _, fixture := range []registryFixture{
		{data, registry.DWORD, nil}, {[]byte{1}, registry.SZ, nil},
		{make([]byte, 65538), registry.SZ, nil}, {[]byte{0, 0, 1, 0}, registry.SZ, nil},
		{nil, registry.SZ, registry.ErrNotExist},
	} {
		if _, _, err := registryString(fixture, "path"); err == nil {
			t.Fatal("Invalid registry value accepted")
		}
	}
	_, _, err = registryString(registryFixture{nil, registry.SZ, registry.ErrNotExist}, "path")
	if !errors.Is(err, registry.ErrNotExist) {
		t.Fatal("Missing selected setting became an unrelated failure")
	}
}
