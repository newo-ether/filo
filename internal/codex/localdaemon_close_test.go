package codex

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"
)

func TestHostCloseInterruptsBlockedSocketWrite(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	socket := &WebSocket{conn: client, reader: bufio.NewReader(client), client: true}
	host := newCodexSocketHost(socket)
	defer host.Close()
	request := make(chan error, 1)
	go func() {
		_, err := host.Rpc.RequestTimeout("model/list", map[string]any{}, time.Second)
		request <- err
	}()
	// Receiving a single frame byte proves a writer has entered conn.Write;
	// the remaining header is deliberately never consumed by the peer.
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := peer.Read(first[:]); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { host.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close waited on a blocked socket write")
	}
	select {
	case err := <-request:
		if err == nil {
			t.Fatal("closed request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close left native request pending")
	}
}

func TestLocalDaemonInvalidConfigurationAndCancelledSetupDoNotDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ConnectLocalDaemon(ctx, LocalDaemonOptions{}); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := ConnectLocalDaemon(context.Background(), LocalDaemonOptions{SocketPath: "relative"}); err == nil {
		t.Fatal("invalid local daemon configuration accepted")
	}
}
