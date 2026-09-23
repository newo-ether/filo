package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const maxRPCWrites = 64
const maxRPCQueuedBytes = 64 << 20

type rpcWriteState uint8

const (
	rpcQueued rpcWriteState = iota
	rpcWriting
	rpcWritten
	rpcCancelled
)

// All write state is protected by Rpc.mu; no I/O takes place under that lock.
type rpcWrite struct {
	data     []byte
	deadline time.Time
	key      string
	state    rpcWriteState
	result   chan error
}

func (r *Rpc) enqueue(message map[string]any, deadline time.Time, key string, reply chan rpcReply) (*rpcWrite, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(message); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closedErr != nil {
		return nil, r.closedErr
	}
	if !time.Now().Before(deadline) {
		return nil, errors.New("Codex request timed out before dispatch")
	}
	if len(r.writes) >= maxRPCWrites || (reply != nil && len(r.pending) >= maxRPCWrites) ||
		buf.Len() > maxRPCQueuedBytes-r.queuedBytes {
		return nil, errors.New("Codex RPC backpressure limit")
	}
	write := &rpcWrite{data: buf.Bytes(), deadline: deadline, key: key, result: make(chan error, 1)}
	r.writes[write] = struct{}{}
	r.queue = append(r.queue, write)
	r.queuedBytes += len(write.data)
	if reply != nil {
		r.pending[key] = reply
	}
	r.signalWriter()
	return write, nil
}

func (r *Rpc) signalWriter() {
	select {
	case r.wakeWriter <- struct{}{}:
	default:
	}
}

func (r *Rpc) send(message map[string]any) error {
	deadline := time.Now().Add(30 * time.Second)
	write, err := r.enqueue(message, deadline, "", nil)
	if err != nil {
		return err
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case err := <-write.result:
		return err
	case <-timer.C:
		r.expire(write, errors.New("Codex write timed out; outcome may be unknown"))
		return <-write.result
	}
}

// expire removes unsent work. If writing has begun its delivery is uncertain:
// retire this connection, never replay it or let a queued expired input run later.
func (r *Rpc) expire(write *rpcWrite, cause error) {
	r.mu.Lock()
	if write.key != "" {
		channel, pending := r.pending[write.key]
		if !pending {
			r.mu.Unlock()
			return // A native response or transport close already settled this call.
		}
		delete(r.pending, write.key)
		channel <- rpcReply{err: cause}
	}
	wasWriting := write.state == rpcWriting
	if write.state == rpcQueued {
		for i, queued := range r.queue {
			if queued == write {
				copy(r.queue[i:], r.queue[i+1:])
				r.queue[len(r.queue)-1] = nil
				r.queue = r.queue[:len(r.queue)-1]
				break
			}
		}
		r.finishWriteLocked(write, cause)
	}
	r.mu.Unlock()
	if wasWriting {
		r.CloseWithError(cause)
	}
}

func (r *Rpc) finishWriteLocked(write *rpcWrite, err error) {
	if _, exists := r.writes[write]; !exists {
		return
	}
	delete(r.writes, write)
	r.queuedBytes -= len(write.data)
	write.data = nil
	if err != nil {
		write.state = rpcCancelled
	} else {
		write.state = rpcWritten
	}
	write.result <- err
}

func (r *Rpc) writeLoop() {
	defer close(r.writerDone)
	for {
		r.mu.Lock()
		if r.closedErr != nil {
			r.mu.Unlock()
			return
		}
		if len(r.queue) == 0 {
			r.mu.Unlock()
			<-r.wakeWriter
			continue
		}
		write := r.queue[0]
		r.queue[0] = nil
		r.queue = r.queue[1:]
		if !time.Now().Before(write.deadline) {
			r.mu.Unlock()
			r.expire(write, errors.New("Codex request timed out before dispatch"))
			continue
		}
		write.state = rpcWriting
		data := write.data
		r.mu.Unlock()
		n, err := r.output.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			r.CloseWithError(err)
			return
		}
		r.mu.Lock()
		r.finishWriteLocked(write, nil)
		r.mu.Unlock()
	}
}
