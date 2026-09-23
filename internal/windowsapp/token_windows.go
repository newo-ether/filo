//go:build windows

package windowsapp

import (
	"context"
	"errors"
	"runtime"

	"golang.org/x/sys/windows"
)

type tokenResult struct {
	value string
	err   error
}

// Package registration queries use the thread's account. SYSTEM's own package
// registry does not describe the selected Desktop user's installation.
func asToken(ctx context.Context, token windows.Token, read func() (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var duplicate windows.Token
	if err := windows.DuplicateTokenEx(token, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &duplicate); err != nil {
		return "", err
	}
	done := make(chan tokenResult, 1)
	go func() {
		defer duplicate.Close()
		done <- onTokenThread(duplicate, read)
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func onTokenThread(token windows.Token, read func() (string, error)) (result tokenResult) {
	runtime.LockOSThread()
	restored := true
	defer func() {
		// If restoring fails, keep this goroutine locked until it exits. Go then
		// destroys that OS thread instead of returning an impersonated thread to
		// the scheduler. Only this disposable Filo thread is affected.
		if restored {
			runtime.UnlockOSThread()
		}
	}()
	var previous windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, true, &previous); err != nil && !errors.Is(err, windows.ERROR_NO_TOKEN) {
		return tokenResult{err: err}
	}
	if previous != 0 {
		defer previous.Close()
	}
	if err := windows.SetThreadToken(nil, token); err != nil {
		return tokenResult{err: err}
	}
	defer func() {
		var err error
		if previous == 0 {
			err = windows.RevertToSelf()
		} else {
			err = windows.SetThreadToken(nil, previous)
		}
		if err != nil {
			restored = false
			result = tokenResult{err: err}
		}
	}()
	result.value, result.err = read()
	return result
}
