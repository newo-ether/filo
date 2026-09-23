// Package windowsapp verifies the running original Desktop without controlling it.
package windowsapp

import (
	"errors"
	"regexp"
)

const desktopFamily = "OpenAI.Codex_2p2nqsd0c76g0"

var ordinarySID = regexp.MustCompile(`^S-1-(?:5-21|12-1)-[0-9]+(?:-[0-9]+)*$`)

type Application struct {
	ProcessID       uint32
	SessionID       uint32
	UserSID         string
	Executable      string
	PackageFullName string
	PackageRoot     string
}

func checkOwner(expectedSID, actualSID, family string, session uint32) error {
	if !ordinarySID.MatchString(expectedSID) || expectedSID != actualSID {
		return errors.New("Desktop IPC server belongs to another Windows account")
	}
	if session == 0 || family != desktopFamily {
		return errors.New("Desktop IPC server is not an original interactive Codex application")
	}
	return nil
}
