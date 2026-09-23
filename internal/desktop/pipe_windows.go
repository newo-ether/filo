//go:build windows

package desktop

import (
	"context"
	"errors"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func DialPipe(ctx context.Context, path string, verify VerifyPipePeer) (net.Conn, error) {
	if err := validatePipe(path, verify); err != nil {
		return nil, err
	}
	// go-winio's default uses anonymous impersonation and cancellable overlapped I/O.
	connection, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = connection.Close()
		}
	}()
	file, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return nil, errors.New("Desktop pipe handle is unavailable")
	}
	var peer uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &peer); err != nil {
		return nil, err
	}
	if peer == 0 {
		return nil, errors.New("Desktop pipe server identity is unavailable")
	}
	// There is no further handle inspection after cancellation can close it.
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	err = verify(ctx, peer)
	stop()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted = true
	return connection, nil
}
