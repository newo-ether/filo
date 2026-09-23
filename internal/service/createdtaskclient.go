package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/newo-ether/filo/internal/protocol"
)

const (
	// taskClientSubscriptions bounds how many task streams one private
	// transport keeps at once.
	taskClientSubscriptions = 4
	// taskClientStatuses bounds how many sessions one status query may list.
	taskClientStatuses = 30
	// taskClientMessages bounds the message count one accepted page may carry.
	taskClientMessages = 128
)

// TaskCall performs one private service call and returns its decoded response as
// raw JSON, so this transport keeps the runtime shape guards the TS client
// applies to `unknown` values.
type TaskCall func(ctx context.Context, path string, input any) (json.RawMessage, error)

// TaskStreamOpen opens one task event stream. The returned response is owned by
// this transport, which closes it with the subscription.
type TaskStreamOpen func(ctx context.Context, path string) (*http.Response, error)

// CreatedTaskClient is the private transport of one executor directory. Only a
// native-acknowledged creation provenance may admit a task to it, so a task the
// desktop did not create never reaches this wire.
type CreatedTaskClient struct {
	directory  string
	call       TaskCall
	openStream TaskStreamOpen
	changed    func(id string)
	mu         sync.Mutex
	// subscriptions holds one live page stream per attached task.
	subscriptions map[string]*taskSubscription
	closed        bool
}

// taskSubscription is one attached task stream: its subscriber count, its
// readiness gate and the last page or terminal failure it observed.
type taskSubscription struct {
	cancel   context.CancelFunc
	refs     int
	ready    chan struct{}
	readyErr error
	page     *protocol.ConversationPage
	err      error
}

// NewCreatedTaskClient builds the private transport of one executor directory.
func NewCreatedTaskClient(directory string, call TaskCall, openStream TaskStreamOpen,
	changed func(id string)) *CreatedTaskClient {
	return &CreatedTaskClient{
		directory:     directory,
		call:          call,
		openStream:    openStream,
		changed:       changed,
		subscriptions: map[string]*taskSubscription{},
	}
}

// Owns reports whether Filo's creation provenance admits this task id. A task
// outside the private executor layout is reported as not owned; an unreadable
// provenance is an error, never an admission.
func (c *CreatedTaskClient) Owns(id string) (bool, error) {
	if err := c.live(); err != nil {
		return false, err
	}
	if !taskIDPattern.MatchString(id) {
		return false, nil
	}
	value, err := ReadExecutorFile(filepath.Join(c.directory, "tasks", id), executorOriginName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := ExecutorReference(value); err != nil {
		return false, err
	}
	return true, nil
}

// live rejects every operation of a closed transport.
func (c *CreatedTaskClient) live() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("Filo task client is closed")
	}
	return nil
}

// admit is the scope gate of every task operation: only an owned task reaches
// the wire, and a closed transport admits nothing.
func (c *CreatedTaskClient) admit(id string) error {
	owns, err := c.Owns(id)
	if err != nil {
		return err
	}
	if !owns {
		return errors.New("Task is outside Filo creation scope")
	}
	return c.live()
}

// Read returns one page of a task. A subscribed task serves the cached activity
// page for the first page, so a reader never re-fetches what the stream already
// delivered.
func (c *CreatedTaskClient) Read(ctx context.Context, id string, cursor *string,
	activity bool) (*protocol.ConversationPage, error) {
	if err := c.admit(id); err != nil {
		return nil, err
	}
	// The TS client tests the cursor for truthiness, so an empty cursor is an
	// absent cursor: it serves the cache and sends no cursor at all.
	if cursor != nil && *cursor == "" {
		cursor = nil
	}
	c.mu.Lock()
	subscription := c.subscriptions[id]
	var cached *protocol.ConversationPage
	var subscriptionErr error
	if subscription != nil {
		subscriptionErr = subscription.err
		if cursor == nil && activity {
			cached = subscription.page
		}
	}
	c.mu.Unlock()
	if subscriptionErr != nil {
		return nil, subscriptionErr
	}
	if cached != nil {
		return cached, nil
	}
	// The query keeps the TS key order: the activity flag first, then the cursor
	// only when the reader provided one.
	query := "includeActivity=" + strconv.FormatBool(activity)
	if cursor != nil {
		query += "&cursor=" + url.QueryEscape(*cursor)
	}
	raw, err := c.call(ctx, "/v1/sessions/"+id+"?"+query, nil)
	if err != nil {
		return nil, err
	}
	return decodeTaskPage(raw)
}

// Statuses returns the status of at most thirty listed sessions, in the order
// they were listed and only for the ones this transport owns.
func (c *CreatedTaskClient) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	if len(ids) > taskClientStatuses {
		return nil, errors.New("At most 30 listed sessions")
	}
	owned := make([]string, 0, len(ids))
	for _, id := range ids {
		owns, err := c.Owns(id)
		if err != nil {
			return nil, err
		}
		if owns {
			owned = append(owned, id)
		}
	}
	if len(owned) == 0 {
		return []protocol.SessionStatus{}, nil
	}
	raw, err := c.call(ctx, "/v1/sessions/status?ids="+strings.Join(owned, ","), nil)
	if err != nil {
		return nil, err
	}
	return decodeTaskStatuses(raw, owned)
}

// Attach subscribes to one task's page stream. The response gate is part of
// attachment: a refused stream fails the attach and leaves nothing subscribed,
// and the stream outlives the caller through its own cancellation.
func (c *CreatedTaskClient) Attach(ctx context.Context, id string) error {
	if err := c.admit(id); err != nil {
		return err
	}
	c.mu.Lock()
	if existing := c.subscriptions[id]; existing != nil {
		existing.refs++
		c.mu.Unlock()
		return awaitTaskReady(ctx, existing)
	}
	if len(c.subscriptions) >= taskClientSubscriptions {
		c.mu.Unlock()
		return errors.New("Too many task subscriptions")
	}
	streamContext, cancel := context.WithCancel(context.Background())
	subscription := &taskSubscription{cancel: cancel, refs: 1, ready: make(chan struct{})}
	c.subscriptions[id] = subscription
	c.mu.Unlock()
	go c.openTaskSubscription(streamContext, id, subscription)
	return awaitTaskReady(ctx, subscription)
}

// Detach releases one subscription. The stream ends with its last subscriber.
func (c *CreatedTaskClient) Detach(id string) {
	c.mu.Lock()
	subscription := c.subscriptions[id]
	released := false
	if subscription != nil {
		subscription.refs--
		if subscription.refs <= 0 {
			delete(c.subscriptions, id)
			released = true
		}
	}
	c.mu.Unlock()
	if released {
		subscription.cancel()
	}
}

// Send submits one message and requires the native acknowledgement to name the
// same client, so an unconfirmed send is never reported as accepted.
func (c *CreatedTaskClient) Send(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	if err := c.admit(id); err != nil {
		return protocol.SendReceipt{}, err
	}
	raw, err := c.call(ctx, "/v1/sessions/"+id+"/messages",
		map[string]any{"text": text, "clientId": clientID})
	if err != nil {
		return protocol.SendReceipt{}, err
	}
	receipt, err := decodeSendReceipt(raw)
	if err != nil || receipt.TurnID == "" || receipt.ClientID != clientID {
		return protocol.SendReceipt{}, errors.New("Native send is unconfirmed")
	}
	return receipt, nil
}

// UpdateSettings applies one settings document and requires the native
// acknowledgement.
func (c *CreatedTaskClient) UpdateSettings(ctx context.Context, id string,
	settings protocol.SessionSettings) error {
	if err := c.admit(id); err != nil {
		return err
	}
	raw, err := c.call(ctx, "/v1/sessions/"+id+"/settings", settings)
	if err != nil {
		return err
	}
	updated, err := decodeTaskFlag(raw, "updated")
	if err != nil || !updated {
		return errors.New("Native settings are unconfirmed")
	}
	return nil
}

// Stop stops one turn and requires the native acknowledgement.
func (c *CreatedTaskClient) Stop(ctx context.Context, id, turnID string) error {
	if err := c.admit(id); err != nil {
		return err
	}
	raw, err := c.call(ctx, "/v1/sessions/"+id+"/stop", map[string]any{"turnId": turnID})
	if err != nil {
		return err
	}
	stopped, err := decodeTaskFlag(raw, "stopped")
	if err != nil || !stopped {
		return errors.New("Native stop is unconfirmed")
	}
	return nil
}

// Close ends every subscription and makes the transport unusable.
func (c *CreatedTaskClient) Close() {
	c.mu.Lock()
	c.closed = true
	subscriptions := make([]*taskSubscription, 0, len(c.subscriptions))
	for _, subscription := range c.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	c.subscriptions = map[string]*taskSubscription{}
	c.mu.Unlock()
	for _, subscription := range subscriptions {
		subscription.cancel()
	}
}

// openTaskSubscription opens one stream and publishes its readiness. A stream
// that cannot be opened is removed again before the attach reports it, so a
// failed subscription never occupies a slot.
func (c *CreatedTaskClient) openTaskSubscription(streamContext context.Context, id string,
	subscription *taskSubscription) {
	response, err := c.openStream(streamContext, "/v1/sessions/"+id+"/events")
	if err == nil {
		switch {
		case streamContext.Err() != nil:
			_ = response.Body.Close()
			err = errors.New("Task subscription cancelled")
		case response.StatusCode < 200 || response.StatusCode >= 300:
			_ = response.Body.Close()
			err = errors.New("Filo task stream is unavailable")
		}
	}
	if err != nil {
		c.mu.Lock()
		if c.subscriptions[id] == subscription {
			delete(c.subscriptions, id)
		}
		subscription.readyErr = err
		c.mu.Unlock()
		subscription.cancel()
		close(subscription.ready)
		return
	}
	close(subscription.ready)
	go c.readTaskSubscription(streamContext, id, subscription, response)
}

// readTaskSubscription publishes every page of one stream and records a terminal
// failure, so a later read reports it instead of serving a stale page.
func (c *CreatedTaskClient) readTaskSubscription(streamContext context.Context, id string,
	subscription *taskSubscription, response *http.Response) {
	err := ReadTaskEvents(response, func(value any) error {
		page, err := taskPage(value)
		if err != nil {
			return err
		}
		c.mu.Lock()
		subscription.page = page
		c.mu.Unlock()
		c.notify(id)
		return nil
	})
	if err == nil || streamContext.Err() != nil {
		return
	}
	c.mu.Lock()
	subscription.page = nil
	subscription.err = err
	c.mu.Unlock()
	c.notify(id)
}

// notify reports one page change without holding the transport lock.
func (c *CreatedTaskClient) notify(id string) {
	if c.changed != nil {
		c.changed(id)
	}
}

// awaitTaskReady waits for one subscription's readiness, bounded by the caller's
// context. The TS client awaits the same promise without a caller cancellation.
func awaitTaskReady(ctx context.Context, subscription *taskSubscription) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-subscription.ready:
		return subscription.readyErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// decodeTaskPage validates one raw page response.
func decodeTaskPage(raw json.RawMessage) (*protocol.ConversationPage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("Filo returned an invalid task page")
	}
	return taskPage(value)
}

// taskPage validates one decoded page exactly as the TS guard does: both
// collections must be present and bounded, and the cursor must be present as
// either text or an explicit null.
func taskPage(value any) (*protocol.ConversationPage, error) {
	invalid := errors.New("Filo returned an invalid task page")
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid
	}
	messages, hasMessages := object["messages"].([]any)
	_, hasQueued := object["queued"].([]any)
	cursor, hasCursor := object["nextCursor"]
	if !hasMessages || !hasQueued || len(messages) > taskClientMessages || !hasCursor {
		return nil, invalid
	}
	if cursor != nil {
		if _, isText := cursor.(string); !isText {
			return nil, invalid
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalid
	}
	var page protocol.ConversationPage
	if err := json.Unmarshal(encoded, &page); err != nil {
		return nil, invalid
	}
	return &page, nil
}

// decodeTaskStatuses validates one scoped status response: every listed and owned
// session answers exactly once, in order.
func decodeTaskStatuses(raw json.RawMessage, owned []string) ([]protocol.SessionStatus, error) {
	invalid := errors.New("Invalid scoped task statuses")
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, invalid
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid
	}
	listed, ok := object["statuses"].([]any)
	if !ok || len(listed) != len(owned) {
		return nil, invalid
	}
	statuses := make([]protocol.SessionStatus, 0, len(listed))
	for index, entry := range listed {
		if _, isObject := entry.(map[string]any); !isObject {
			return nil, invalid
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, invalid
		}
		var status protocol.SessionStatus
		if err := json.Unmarshal(encoded, &status); err != nil || status.ID != owned[index] {
			return nil, invalid
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// decodeSendReceipt decodes one send response.
func decodeSendReceipt(raw json.RawMessage) (protocol.SendReceipt, error) {
	var receipt protocol.SendReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return protocol.SendReceipt{}, err
	}
	return receipt, nil
}

// decodeTaskFlag reads one boolean acknowledgement flag of a task response.
func decodeTaskFlag(raw json.RawMessage, name string) (bool, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	flag, ok := value[name].(bool)
	if !ok {
		return false, errors.New("Missing native acknowledgement")
	}
	return flag, nil
}
