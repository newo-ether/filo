//go:build windows

package windowsapp

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxManifestBytes = 1 << 20

func validateExecutable(root, executable string) error {
	root, err := localPath(root)
	if err != nil {
		return err
	}
	executable, err = localPath(executable)
	if err != nil {
		return err
	}
	file, err := os.Open(filepath.Join(root, "AppxManifest.xml"))
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxManifestBytes {
		return errors.New("Desktop application manifest is too large")
	}
	var manifest struct {
		XMLName  xml.Name `xml:"Package"`
		Identity struct {
			Name string `xml:"Name,attr"`
		} `xml:"Identity"`
		Applications []struct {
			Executable string `xml:"Executable,attr"`
		} `xml:"Applications>Application"`
	}
	if err := xml.Unmarshal(body, &manifest); err != nil {
		return err
	}
	if manifest.Identity.Name != "OpenAI.Codex" {
		return errors.New("Desktop manifest identity does not match")
	}
	for _, application := range manifest.Applications {
		if !filepath.IsLocal(application.Executable) {
			continue
		}
		candidate := filepath.Join(root, application.Executable)
		if !strings.EqualFold(candidate, executable) {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return nil
		}
	}
	return errors.New("Desktop IPC server does not match its registered application executable")
}

func localPath(path string) (string, error) {
	path = strings.TrimPrefix(path, `\\?\`)
	volume := filepath.VolumeName(path)
	if !filepath.IsAbs(path) || len(volume) != 2 || volume[1] != ':' {
		return "", errors.New("Desktop application path must be an absolute local path")
	}
	return filepath.Clean(path), nil
}
