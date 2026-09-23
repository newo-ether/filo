// Package codex talks to the public Codex App Server over JSONL. Transport
// failures and RPC error objects stay distinct: a rejected request never closes
// a healthy connection, and a malformed line never leaves a pending call waiting.
package codex

import (
	"bufio"
	"strings"

	"errors"
	"fmt"
	"github.com/newo-ether/filo/internal/nativejson"
	"io"
	"math"
	"strconv"
	"sync"
	"time"
)

// RpcError mirrors CodexRpcError: an RPC error object carried by the transport.
// Code is nil when the native error object omitted it.
type RpcError struct {
	Message string
	Code    *float64
}

func (e *RpcError) Error() string { return e.Message }

type rpcReply struct {
	value any
	err   error
}

// Rpc is a bidirectional JSONL transport with one serialized writer. Close closes
// only the supplied transport endpoints when they implement io.Closer. Blocking
// endpoints must support Close to release Read/Write; non-closable endpoints must
// finish their own I/O. The caller must supply Filo-owned endpoints, never a
// process-lifetime control for the original Desktop or an accepted native turn.
type Rpc struct {
	reader *bufio.Reader
	input  io.Reader
	mu     sync.Mutex
	nextId int64
	output io.Writer
	// Handlers receive server-originated packets and close causes. Callbacks run
	// on the reader goroutine and must not block on this Rpc's own responses.
	onRequest      func(packet map[string]any)
	onNotification func(packet map[string]any)
	onClosed       func(cause error)
	closedErr      error
	pending        map[string]chan rpcReply
	writes         map[*rpcWrite]struct{}
	queue          []*rpcWrite
	queuedBytes    int
	wakeWriter     chan struct{}
	readerDone     chan struct{}
	writerDone     chan struct{}
}

// SetHandlers installs request and notification callbacks safely while reading.
func (r *Rpc) SetHandlers(onRequest, onNotification func(packet map[string]any)) {
	r.mu.Lock()
	r.onRequest, r.onNotification = onRequest, onNotification
	r.mu.Unlock()
}

// SetClosedHandler installs the close callback safely.
func (r *Rpc) SetClosedHandler(onClosed func(cause error)) {
	r.mu.Lock()
	r.onClosed = onClosed
	cause := r.closedErr
	r.mu.Unlock()
	if cause != nil && onClosed != nil {
		onClosed(cause)
	}
}

func NewRpc(input io.Reader, output io.Writer) *Rpc {
	r := &Rpc{reader: bufio.NewReader(input), input: input, output: output,
		pending: make(map[string]chan rpcReply), writes: make(map[*rpcWrite]struct{}),
		wakeWriter: make(chan struct{}, 1), readerDone: make(chan struct{}), writerDone: make(chan struct{})}
	go r.writeLoop()
	go r.readLoop()
	return r
}

// Request sends one request with the default 30 second native timeout.
func (r *Rpc) Request(method string, params any) (any, error) {
	return r.RequestTimeout(method, params, 30*time.Second)
}

func (r *Rpc) RequestTimeout(method string, params any, timeout time.Duration) (any, error) {
	deadline := time.Now().Add(timeout)
	r.mu.Lock()
	if r.closedErr != nil {
		err := r.closedErr
		r.mu.Unlock()
		return nil, err
	}
	r.nextId++
	id := r.nextId
	r.mu.Unlock()
	key := fmt.Sprintf("n:%d", id)
	channel := make(chan rpcReply, 1)
	write, err := r.enqueue(map[string]any{"id": id, "method": method, "params": params}, deadline, key, channel)
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case reply := <-channel:
		return reply.value, reply.err
	case <-timer.C:
		r.expire(write, fmt.Errorf("Codex request timed out: %s", method))
		reply := <-channel
		return reply.value, reply.err
	}
}

func (r *Rpc) Notify(method string, params any) error {
	if params == nil {
		params = map[string]any{}
	}
	return r.send(map[string]any{"method": method, "params": params})
}

func (r *Rpc) Respond(id any, result any) error {
	return r.send(map[string]any{"id": id, "result": result})
}

func (r *Rpc) RejectRequest(id any, message string) error {
	return r.RejectRequestCode(id, message, -32601)
}

func (r *Rpc) RejectRequestCode(id any, message string, code float64) error {
	return r.send(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
}

// Close rejects every pending call and blocks future sends. The first cause wins.
func (r *Rpc) Close() { r.CloseWithError(errors.New("Codex transport closed")) }

func (r *Rpc) CloseWithError(cause error) {
	if cause == nil {
		cause = errors.New("Codex transport closed")
	}
	r.mu.Lock()
	if r.closedErr != nil {
		r.mu.Unlock()
		return
	}
	r.closedErr = cause
	pending := r.pending
	r.pending = make(map[string]chan rpcReply)
	for _, channel := range pending {
		channel <- rpcReply{err: cause}
	}
	for write := range r.writes {
		r.finishWriteLocked(write, cause)
	}
	r.queue = nil
	r.queuedBytes = 0
	onClosed := r.onClosed
	r.mu.Unlock()
	r.signalWriter()
	// Transport closure interrupts a stalled writer without waiting for its lock.
	// These are disposable endpoints; native process cleanup belongs to its owner.
	if closer, ok := r.output.(io.Closer); ok {
		_ = closer.Close()
	}
	if closer, ok := r.input.(io.Closer); ok {
		_ = closer.Close()
	}
	if onClosed != nil {
		onClosed(cause)
	}
}

func (r *Rpc) readLoop() {
	defer close(r.readerDone)
	for {
		line, err := r.readLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.CloseWithError(err)
			} else {
				r.Close()
			}
			return
		}
		if err := r.dispatch(line); err != nil {
			r.CloseWithError(err)
			return
		}
	}
}

// readLine assembles one JSONL line of unbounded length. A final line without a
// trailing newline is delivered once, then EOF is reported.
func (r *Rpc) readLine() (string, error) {
	var buf []byte
	for {
		chunk, err := r.reader.ReadString('\n')
		buf = append(buf, chunk...)
		if err == nil {
			line := string(buf[:len(buf)-1])
			return trimCarriageReturn(line), nil
		}
		if !errors.Is(err, io.EOF) {
			return "", err
		}
		if len(buf) == 0 {
			return "", io.EOF
		}
		remainder := trimCarriageReturn(string(buf))
		buf = nil
		if remainder == "" {
			return "", io.EOF
		}
		return remainder, nil
	}
}

func trimCarriageReturn(line string) string {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

func (r *Rpc) dispatch(line string) error {
	message, _, err := nativejson.Read(strings.NewReader(line), false)
	if err != nil {
		return errors.New("Invalid Codex RPC envelope")
	}
	packet, ok := nativejson.Fields(message)
	if !ok {
		return errors.New("Invalid Codex RPC envelope")
	}
	if _, isMethod := packet["method"].(string); isMethod {
		r.mu.Lock()
		onRequest, onNotification := r.onRequest, r.onNotification
		r.mu.Unlock()
		if _, hasId := packet["id"]; hasId {
			if onRequest != nil {
				onRequest(packet)
			}
		} else if onNotification != nil {
			onNotification(packet)
		}
		return nil
	}
	key, ok := responseKey(packet["id"])
	if !ok {
		return errors.New("Missing Codex RPC response id")
	}
	r.mu.Lock()
	channel, found := r.pending[key]
	delete(r.pending, key)
	r.mu.Unlock()
	if !found {
		return nil
	}
	if rawError, isErr := packet["error"]; isErr && rawError != nil {
		channel <- rpcReply{err: decodeRpcError(rawError)}
		return nil
	}
	if result, hasResult := packet["result"]; hasResult {
		channel <- rpcReply{value: result}
		return nil
	}
	channel <- rpcReply{err: errors.New("Missing Codex RPC result")}
	return nil
}

func decodeRpcError(rawError any) *RpcError {
	fields, _ := nativejson.Fields(rawError)
	var code *float64
	if rawCode, has := fields["code"]; has {
		if value, isNumber := rawCode.(float64); isNumber {
			code = &value
		}
	}
	message := ""
	if rawMessage, has := fields["message"]; has {
		if text, isString := rawMessage.(string); isString {
			message = text
		}
	}
	if message == "" {
		// Matches `Codex RPC error ${error.code}` including the undefined case.
		if code != nil {
			message = "Codex RPC error " + formatCode(*code)
		} else {
			message = "Codex RPC error undefined"
		}
	}
	return &RpcError{Message: message, Code: code}
}

func responseKey(id any) (string, bool) {
	switch value := id.(type) {
	case float64:
		return "n:" + formatCode(value), true
	case string:
		return "s:" + value, true
	default:
		return "", false
	}
}

func formatCode(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) <= 9007199254740991 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
