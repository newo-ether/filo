//go:build !darwin

package service

import "errors"

func runNativeHost([]string) error { return errors.New("FILO_HOST_PLATFORM: macOS required") }
