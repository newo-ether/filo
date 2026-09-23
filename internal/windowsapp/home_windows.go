//go:build windows

package windowsapp

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

var homeVariable = regexp.MustCompile(`%([^%]+)%`)

// NativeHome resolves only the selected original account. It can read persisted
// history while Desktop is closed, without running a helper or opening a writer.
func NativeHome(ctx context.Context, expectedSID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !ordinarySID.MatchString(expectedSID) {
		return "", errors.New("An ordinary Windows account is required")
	}
	profileKey, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\`+expectedSID, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	profile, kind, err := registryString(profileKey, "ProfileImagePath")
	profileKey.Close()
	if err != nil {
		return "", err
	}
	if kind == registry.EXPAND_SZ {
		profile, err = registry.ExpandString(profile)
		if err != nil {
			return "", err
		}
	}
	var custom string
	environment, err := registry.OpenKey(registry.USERS, expectedSID+`\Environment`, registry.QUERY_VALUE)
	if err == nil {
		custom, _, err = registryString(environment, "CODEX_HOME")
		environment.Close()
	}
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return resolveHome(profile, custom, os.Getenv("SystemRoot"), os.Getenv("ProgramData"))
}

type registryValue interface {
	GetValue(string, []byte) (int, uint32, error)
}

func registryString(key registryValue, name string) (string, uint32, error) {
	buffer := make([]byte, 65536)
	n, kind, err := key.GetValue(name, buffer)
	if err != nil {
		return "", kind, err
	}
	if kind != registry.SZ && kind != registry.EXPAND_SZ {
		return "", kind, registry.ErrUnexpectedType
	}
	if n < 0 || n > len(buffer) || n%2 != 0 {
		return "", kind, errors.New("Invalid original path registry value")
	}
	units := make([]uint16, n/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(buffer[i*2:])
	}
	if len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	value := string(utf16.Decode(units))
	if strings.ContainsRune(value, 0) {
		return "", kind, errors.New("Invalid original path registry value")
	}
	return value, kind, nil
}

func resolveHome(profile, custom, systemRoot, programData string) (string, error) {
	if !filepath.IsAbs(profile) {
		return "", errors.New("Original Windows profile must be absolute")
	}
	home := filepath.Join(profile, ".codex")
	if custom != "" {
		volume := filepath.VolumeName(profile)
		values := map[string]string{
			"USERPROFILE": profile, "LOCALAPPDATA": filepath.Join(profile, "AppData", "Local"),
			"APPDATA": filepath.Join(profile, "AppData", "Roaming"), "HOMEDRIVE": volume,
			"HOMEPATH": profile[len(volume):], "SYSTEMROOT": systemRoot, "PROGRAMDATA": programData,
		}
		var unsupported bool
		home = homeVariable.ReplaceAllStringFunc(custom, func(match string) string {
			value, ok := values[strings.ToUpper(match[1:len(match)-1])]
			if !ok {
				unsupported = true
			}
			return value
		})
		if unsupported {
			return "", errors.New("Unsupported variable in original Codex home")
		}
	}
	if !filepath.IsAbs(home) || strings.ContainsRune(home, 0) || len(utf16.Encode([]rune(home))) >= 32768 {
		return "", errors.New("Original Codex home must be an absolute bounded path")
	}
	return filepath.Abs(home)
}
