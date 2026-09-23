package desktop

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

type Message map[string]any

// Dial must return only a verified original Desktop connection. This client never
// launches a router, acquires a task writer, or signals an external process.
// Close on the returned stream must unblock Read and Write.
type Options struct {
	Dial          func(context.Context) (io.ReadWriteCloser, error)
	BeforeConnect func(context.Context) error
	Timeout       time.Duration
	// Callbacks run without transport locks. Like the native event handlers they
	// replace, they must return without waiting for an IPC response.
	OnBroadcast func(Message)
	OnClosed    func()
}

type Status struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type opening struct {
	done chan struct{}
	err  error
}

type Client struct {
	mu        sync.Mutex
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	socket    *exchange
	opening   *opening
	connected bool
	stopped   bool
	failure   error
	retryAt   time.Time
}

func NewClient(options Options) *Client {
	if options.Timeout <= 0 {
		options.Timeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{options: options, ctx: ctx, cancel: cancel}
}

func (c *Client) ConnectionStatus() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := Status{State: "disconnected"}
	switch {
	case c.connected:
		status.State = "connected"
	case c.failure != nil:
		status.State, status.Error = "unavailable", c.failure.Error()
	case c.opening != nil:
		status.State = "connecting"
	}
	return status
}

func (c *Client) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return errors.New("Desktop IPC is closed")
	}
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	if c.opening == nil && time.Now().Before(c.retryAt) {
		err := c.failure
		c.mu.Unlock()
		return err
	}
	if c.opening == nil {
		c.opening = &opening{done: make(chan struct{})}
		go c.open(c.opening)
	}
	attempt := c.opening
	c.mu.Unlock()
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return errors.New("Desktop IPC is closed")
	}
}

func (c *Client) open(attempt *opening) {
	err := c.initialize()
	c.mu.Lock()
	if err == nil {
		if c.socket == nil {
			err = errors.New("Original desktop disconnected")
		} else {
			select {
			case <-c.socket.done:
				err = errors.New("Original desktop disconnected")
			default:
			}
		}
	}
	if c.stopped && err == nil {
		err = errors.New("Desktop IPC is closed")
	}
	c.connected = err == nil
	c.failure = err
	if err != nil {
		c.retryAt = time.Now().Add(time.Second)
	} else {
		c.retryAt = time.Time{}
	}
	c.opening = nil
	attempt.err = err
	close(attempt.done)
	c.mu.Unlock()
}

func (c *Client) initialize() error {
	ctx := c.ctx
	if c.options.BeforeConnect != nil {
		if err := c.options.BeforeConnect(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.options.Dial == nil {
		return errors.New("Desktop IPC connector is unavailable")
	}
	dialCtx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	wire, err := c.options.Dial(dialCtx)
	cancel()
	if err != nil {
		return err
	}
	x := newExchange(wire, c.options.Timeout, c.options.OnBroadcast)
	x.onClose = func() {
		c.mu.Lock()
		current := c.socket == x
		if current {
			c.socket, c.connected = nil, false
		}
		c.mu.Unlock()
		if current && c.options.OnClosed != nil {
			c.options.OnClosed()
		}
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		x.close(errors.New("Desktop IPC is closed"))
		return errors.New("Desktop IPC is closed")
	}
	c.socket = x
	c.mu.Unlock()
	go x.readLoop()
	go x.writeLoop()
	reply, err := x.call(ctx, "initialize", 0, map[string]any{"clientType": "filo"}, "")
	if err == nil {
		result, _ := nativejson.Fields(reply["result"])
		id, valid := nativejson.AsText(result["clientId"])
		if text(reply, "resultType") != "success" || !valid {
			err = errors.New("Desktop IPC initialization rejected")
		} else {
			x.mu.Lock()
			x.clientID = id
			err = x.failure
			x.mu.Unlock()
		}
	}
	if err != nil {
		x.close(err)
	}
	return err
}

func (c *Client) Request(ctx context.Context, method string, version int, params any, targetClientID string) (Message, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	x := c.socket
	if !c.connected {
		x = nil
	}
	c.mu.Unlock()
	if x == nil {
		return nil, errors.New("Original desktop is unavailable")
	}
	return x.call(ctx, method, version, params, targetClientID)
}

func (c *Client) Broadcast(method string, version int, params any, targets []string) error {
	c.mu.Lock()
	x := c.socket
	if !c.connected {
		x = nil
	}
	c.mu.Unlock()
	if x == nil {
		return errors.New("Original desktop is unavailable")
	}
	x.mu.Lock()
	id := x.clientID
	x.mu.Unlock()
	message := Message{"type": "broadcast", "sourceClientId": id, "method": method, "version": version, "params": params}
	if targets != nil {
		message["targetClientIds"] = targets
	}
	return x.enqueue(message, "")
}

func (c *Client) Close() {
	c.mu.Lock()
	c.stopped, c.connected = true, false
	x := c.socket
	c.cancel()
	c.mu.Unlock()
	if x != nil {
		x.close(errors.New("Desktop IPC is closed"))
	}
}

func text(message map[string]any, key string) string {
	value, _ := nativejson.AsText(message[key])
	return value
}
