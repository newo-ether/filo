//go:build windows

package service

import (
	"errors"

	"golang.org/x/sys/windows"
)

// verifySourceUser prevents an incorrectly registered helper from opening a
// selected user's native endpoints under SYSTEM or another Windows account.
func verifySourceUser(expectedSID string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if !systemServiceSIDPattern.MatchString(expectedSID) || err != nil || user.User.Sid.String() != expectedSID {
		return errors.New("Filo source helper must run as its selected native Windows user")
	}
	return nil
}
