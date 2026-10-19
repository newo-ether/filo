package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func taskRecoveryRefs(client *CreatedTaskClient, id string) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	if subscription := client.subscriptions[id]; subscription != nil {
		return subscription.refs.count
	}
	return 0
}

func TestCreatedTaskClientReadRecoversWithoutLosingReferences(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	emit := fixture.openScriptedStream()
	for range 2 {
		if err := fixture.client.Attach(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	emit("event: error\n\n")
	waitForCondition(t, time.Second, "failed stream", func() bool { return fixture.subscriptionError(task) != nil })
	fixture.scriptStatus(http.StatusServiceUnavailable)
	if _, err := fixture.client.Read(context.Background(), task, nil, true); err == nil {
		t.Fatal("a refused replacement was hidden")
	}
	if refs := taskRecoveryRefs(fixture.client, task); refs != 2 {
		t.Fatalf("failed replacement lost references: %d", refs)
	}
	fixture.blockStream()
	fixture.answer = func(_ string, input any) (json.RawMessage, error) {
		if input != nil {
			t.Error("recovery replayed a mutation")
		}
		return json.RawMessage(taskPagePayload("recovered")), nil
	}
	page, err := fixture.client.Read(context.Background(), task, nil, true)
	if err != nil || page == nil || *page.NextCursor != "recovered" {
		t.Fatalf("recovered read = %+v, %v", page, err)
	}
	if len(fixture.openList()) != 3 || len(fixture.snapshotCalls()) != 1 {
		t.Fatalf("recovery was not one attempt per read: %v / %v", fixture.openList(), fixture.snapshotCalls())
	}
	fixture.client.Detach(task)
	if refs := taskRecoveryRefs(fixture.client, task); refs != 1 {
		t.Fatalf("first detach lost a subscriber: %d", refs)
	}
	fixture.client.Detach(task)
	if fixture.subscriptionCount() != 0 {
		t.Fatal("last detach retained the replacement")
	}
}

func TestCreatedTaskClientAttachReplacesFailedStream(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	fixture.scriptStream("event: error\n\n")
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, time.Second, "failed stream", func() bool { return fixture.subscriptionError(task) != nil })
	fixture.blockStream()
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if refs := taskRecoveryRefs(fixture.client, task); refs != 2 || len(fixture.openList()) != 2 {
		t.Fatalf("replacement references/opens = %d / %v", refs, fixture.openList())
	}
	fixture.client.Detach(task)
	fixture.client.Detach(task)
	if fixture.subscriptionCount() != 0 {
		t.Fatal("replacement did not release")
	}
}

func TestCreatedTaskClientCancelledAttachClosesReader(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	opened, finished := make(chan struct{}), make(chan struct{})
	fixture.client.openStream = func(ctx context.Context, _ string) (*http.Response, error) {
		close(opened)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- fixture.client.Attach(ctx, task) }()
	<-opened
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled attach = %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled attach leaked its open operation")
	}
	if fixture.subscriptionCount() != 0 {
		t.Fatal("cancelled attach retained its reference")
	}
}

type taskRecoveryBody struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (body *taskRecoveryBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(func() { close(body.closed) })
	return err
}

func TestCreatedTaskClientDetachClosesBlockedBody(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &taskRecoveryBody{ReadCloser: reader, closed: make(chan struct{})}
	fixture.client.openStream = func(_ context.Context, _ string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{taskEventStreamType}}, Body: body}, nil
	}
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	fixture.client.Detach(task)
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("detach leaked its blocked reader")
	}
}

func TestCreatedTaskClientIgnoresStaleStreamPagesAndFailures(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	fixture.blockStream()
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	fixture.client.mu.Lock()
	current := fixture.client.subscriptions[task]
	fixture.client.mu.Unlock()
	stale := &taskSubscription{}
	for _, body := range []string{"data: " + taskPagePayload("stale") + "\n\n", "event: error\n\n"} {
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{taskEventStreamType}},
			Body: io.NopCloser(strings.NewReader(body))}
		fixture.client.readTaskSubscription(context.Background(), task, stale, response)
	}
	fixture.client.mu.Lock()
	unchanged := fixture.client.subscriptions[task] == current && current.err == nil && current.page == nil
	fixture.client.mu.Unlock()
	if !unchanged || len(fixture.changeList()) != 0 {
		t.Fatal("a stale reader changed or notified the replacement")
	}
}

func TestCreatedTaskClientConcurrentReadsShareOneRecovery(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	fixture.scriptStream("event: error\n\n")
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, time.Second, "failed stream", func() bool { return fixture.subscriptionError(task) != nil })
	fixture.blockStream()
	fixture.answer = func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(taskPagePayload("recovered")), nil
	}
	start := make(chan struct{})
	results := make(chan error, 12)
	for range cap(results) {
		go func() {
			<-start
			_, err := fixture.client.Read(context.Background(), task, nil, true)
			results <- err
		}()
	}
	close(start)
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.openList()) != 2 || taskRecoveryRefs(fixture.client, task) != 1 {
		t.Fatalf("concurrent readers multiplied streams/references: %v", fixture.openList())
	}
}

func TestCreatedTaskClientCancelledSharedAttachKeepsOwnerReference(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	fixture.scriptStream("event: error\n\n")
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, time.Second, "failed stream", func() bool { return fixture.subscriptionError(task) != nil })
	opened, release := make(chan struct{}), make(chan struct{})
	reader, writer := io.Pipe()
	defer writer.Close()
	fixture.client.openStream = func(ctx context.Context, _ string) (*http.Response, error) {
		close(opened)
		select {
		case <-release:
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{taskEventStreamType}}, Body: reader}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- fixture.client.Attach(ctx, task) }()
	<-opened
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled shared attach = %v", err)
	}
	if refs := taskRecoveryRefs(fixture.client, task); refs != 1 {
		t.Fatalf("cancel removed an existing owner: %d", refs)
	}
	close(release)
	fixture.client.Detach(task)
	if fixture.subscriptionCount() != 0 {
		t.Fatal("remaining owner could not release replacement")
	}
}
