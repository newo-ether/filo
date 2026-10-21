//go:build !windows

package service

import "errors"

func verifySourceUser(string) error {
	return errors.New("Windows native sources are unavailable on this platform")
}
