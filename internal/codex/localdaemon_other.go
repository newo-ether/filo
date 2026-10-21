//go:build !windows && !linux

package codex

import (
	"errors"
	"net"
)

func validateDaemonSocket(string, string) (string, func(), error) {
	return "", nil, errors.New("Verified local Codex daemon sockets are unsupported on this platform")
}

func verifyDaemonPeer(net.Conn, string) error {
	return errors.New("Verified local Codex daemon sockets are unsupported on this platform")
}
