//go:build !windows

package desktop

import (
	"context"
	"errors"
	"net"
)

func DialPipe(context.Context, string, VerifyPipePeer) (net.Conn, error) {
	return nil, errors.New("Windows named pipes are unavailable on this platform")
}
