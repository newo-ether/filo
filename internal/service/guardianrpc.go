package service

import (
	"context"
	"github.com/newo-ether/filo/internal/nativejson"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

// guardianRPC holds only a read-oriented subscription to one authenticated host.
// It neither creates a task nor sends an input.
type guardianRPC struct {
	rpc    *codex.Rpc
	socket *codex.WebSocket
	reader *io.PipeReader
	writer *io.PipeWriter
	closed atomic.Bool
	once   sync.Once
}

func connectGuardian(url, token string, notification func(map[string]any)) (*guardianRPC, error) {
	socket, err := codex.DialWebSocket(context.Background(), url,
		http.Header{"Authorization": {"Bearer " + token}}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	c := &guardianRPC{socket: socket, reader: reader, writer: writer}
	c.rpc = codex.NewRpc(reader, guardianWriter{socket})
	c.rpc.SetClosedHandler(func(error) { c.closed.Store(true) })
	c.rpc.SetHandlers(func(packet map[string]any) {
		_ = c.rpc.RejectRequest(packet["id"], "Remote approval/input is unavailable")
	}, notification)
	go func() {
		defer c.Close()
		for {
			body, err := socket.ReadSmallMessage(1 << 20)
			if err != nil {
				return
			}
			if _, err := writer.Write(append(body, '\n')); err != nil {
				return
			}
		}
	}()
	_, err = c.request("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "filo_native_lifetime", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	})
	if err == nil {
		err = c.rpc.Notify("initialized", nil)
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *guardianRPC) request(method string, params any) (map[string]any, error) {
	value, err := c.rpc.RequestTimeout(method, params, 10*time.Second)
	if err != nil {
		return nil, err
	}
	result, ok := nativejson.Fields(value)
	if !ok {
		return nil, errGuardianState
	}
	return result, nil
}

func (c *guardianRPC) Close() {
	c.once.Do(func() {
		c.closed.Store(true)
		c.socket.Terminate()
		_ = c.reader.Close()
		_ = c.writer.Close()
		c.rpc.Close()
	})
}

type guardianWriter struct{ socket *codex.WebSocket }

func (w guardianWriter) Write(body []byte) (int, error) {
	_ = w.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := w.socket.WriteText([]byte(strings.TrimSpace(string(body))))
	return len(body), err
}
