//go:build !windows

package windowsapp

import (
	"context"
	"errors"
)

func Inspect(context.Context, uint32, string) (Application, error) {
	return Application{}, errors.New("Windows application identities are unavailable on this platform")
}
