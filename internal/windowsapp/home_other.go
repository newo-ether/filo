//go:build !windows

package windowsapp

import (
	"context"
	"errors"
)

func NativeHome(context.Context, string) (string, error) {
	return "", errors.New("Windows original account discovery is unavailable on this platform")
}
