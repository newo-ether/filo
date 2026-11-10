//go:build !windows && !darwin

package desktop

import (
	"context"
	"errors"
	"net"
)

const DefaultPipePath = `\\.\pipe\codex-ipc`

func DialPipe(context.Context, string, VerifyPipePeer) (net.Conn, error) {
	return nil, errors.New("Windows named pipes are unavailable on this platform")
}
