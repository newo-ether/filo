package desktop

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

type response struct {
	message Message
	err     error
}

type outgoing struct {
	body []byte
	id   string
}

type pendingCall struct {
	ctx    context.Context
	result chan response
}

type exchange struct {
	mu        sync.Mutex
	wire      io.ReadWriteCloser
	timeout   time.Duration
	clientID  string
	pending   map[string]*pendingCall
	writes    chan outgoing
	queued    int
	done      chan struct{}
	failure   error
	onClose   func()
	broadcast func(Message)
}

func newExchange(wire io.ReadWriteCloser, timeout time.Duration, broadcast func(Message)) *exchange {
	return &exchange{wire: wire, timeout: timeout, clientID: "filo", pending: make(map[string]*pendingCall),
		writes: make(chan outgoing, 64), done: make(chan struct{}), broadcast: broadcast}
}

func (x *exchange) close(err error) {
	x.mu.Lock()
	if x.failure != nil {
		x.mu.Unlock()
		return
	}
	x.failure = err
	close(x.done)
	for id, call := range x.pending {
		call.result <- response{err: err}
		delete(x.pending, id)
	}
	x.queued = 0
drain:
	for {
		select {
		case <-x.writes:
		default:
			break drain
		}
	}
	x.mu.Unlock()
	_ = x.wire.Close()
	if x.onClose != nil {
		x.onClose()
	}
}

func (x *exchange) call(ctx context.Context, method string, version int, params any, target string) (Message, error) {
	ctx, cancel := context.WithTimeout(ctx, x.timeout)
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	nonce[6], nonce[8] = nonce[6]&15|64, nonce[8]&63|128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", nonce[:4], nonce[4:6], nonce[6:8], nonce[8:10], nonce[10:])
	call := make(chan response, 1)
	x.mu.Lock()
	if x.failure != nil {
		err := x.failure
		x.mu.Unlock()
		return nil, err
	}
	if len(x.pending) >= 64 {
		x.mu.Unlock()
		return nil, errors.New("Too many desktop requests")
	}
	x.pending[id] = &pendingCall{ctx, call}
	source := x.clientID
	x.mu.Unlock()
	defer func() { x.mu.Lock(); delete(x.pending, id); x.mu.Unlock() }()
	message := Message{"type": "request", "requestId": id, "sourceClientId": source,
		"method": method, "version": version, "params": params, "timeoutMs": x.timeout.Milliseconds()}
	if target != "" {
		message["targetClientId"] = target
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := x.enqueue(message, id); err != nil {
		return nil, err
	}
	select {
	case result := <-call:
		return result.message, result.err
	case <-ctx.Done():
		return nil, fmt.Errorf("Desktop request timed out or cancelled; do not automatically resend: %w", ctx.Err())
	}
}

func (x *exchange) enqueue(message Message, id string) error {
	body, err := frameBody(message)
	if err != nil {
		return err
	}
	x.mu.Lock()
	if x.failure != nil {
		err := x.failure
		x.mu.Unlock()
		return err
	}
	if x.queued+len(body) <= MaxOutputBytes && len(x.writes) < cap(x.writes) {
		x.queued += len(body)
		x.writes <- outgoing{body, id}
		x.mu.Unlock()
		return nil
	}
	x.mu.Unlock()
	err = errors.New("Desktop IPC backpressure limit")
	x.close(err)
	return err
}

func (x *exchange) writeLoop() {
	for {
		select {
		case <-x.done:
			return
		case packet := <-x.writes:
			x.mu.Lock()
			pending := x.pending[packet.id]
			active := x.failure == nil && (packet.id == "" || pending != nil && pending.ctx.Err() == nil)
			x.mu.Unlock()
			var err error
			if active {
				err = writeFrameBody(x.wire, packet.body)
			}
			x.mu.Lock()
			if x.failure == nil {
				x.queued -= len(packet.body)
			}
			x.mu.Unlock()
			if err != nil {
				x.close(err)
				return
			}
		}
	}
}

func (x *exchange) readLoop() {
	for {
		value, err := ReadFrame(x.wire)
		if err != nil {
			x.close(errors.New("Original desktop disconnected; request outcome may be unknown"))
			return
		}
		message, valid := nativejson.Fields(value)
		_, typed := nativejson.AsText(message["type"])
		if !valid || !typed {
			x.close(errors.New("Invalid IPC message"))
			return
		}
		switch text(message, "type") {
		case "client-discovery-request":
			reply := Message{"type": "client-discovery-response", "response": map[string]any{"canHandle": false}}
			if id, exists := message["requestId"]; exists {
				reply["requestId"] = id
			}
			if err := x.enqueue(reply, ""); err != nil {
				x.close(err)
				return
			}
		case "response":
			id := text(message, "requestId")
			x.mu.Lock()
			if call := x.pending[id]; call != nil {
				delete(x.pending, id)
				call.result <- response{message: message}
			}
			x.mu.Unlock()
		case "broadcast":
			if x.broadcast != nil {
				x.broadcast(message)
			}
		}
	}
}
