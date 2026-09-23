package desktop

import (
	"context"
	"errors"
	"strings"
)

const DefaultPipePath = `\\.\pipe\codex-ipc`

// VerifyPipePeer must verify the selected account, interactive session and
// registered original application. It runs against the actual connected peer,
// before any IPC initialization or task data is written.
type VerifyPipePeer func(context.Context, uint32) error

func validatePipe(path string, verify VerifyPipePeer) error {
	const prefix = `\\.\pipe\`
	if verify == nil {
		return errors.New("Desktop pipe identity verification is required")
	}
	if !strings.HasPrefix(strings.ToLower(path), prefix) || len(path) == len(prefix) ||
		strings.ContainsAny(path[len(prefix):], "\\/\x00") {
		return errors.New("Desktop IPC requires a local named pipe")
	}
	return nil
}
