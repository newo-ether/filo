package desktop

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

func success(w io.Writer, request Message, result any) error {
	return WriteFrame(w, Message{"type": "response", "requestId": request["requestId"],
		"resultType": "success", "handledByClientId": "owner", "result": result})
}

func fixture(t *testing.T, timeout time.Duration, handle func(net.Conn, Message)) (*Client, <-chan Message, *atomic.Int32) {
	t.Helper()
	requests := make(chan Message, 64)
	dials := &atomic.Int32{}
	client := NewClient(Options{Timeout: timeout, Dial: func(context.Context) (io.ReadWriteCloser, error) {
		local, peer := net.Pipe()
		dials.Add(1)
		t.Cleanup(func() { _ = peer.Close() })
		go func() {
			defer peer.Close()
			for {
				value, err := ReadFrame(peer)
				if err != nil {
					return
				}
				message, _ := nativejson.Fields(value)
				requests <- message
				if text(message, "method") == "initialize" {
					if success(peer, message, map[string]any{"clientId": "filo-client"}) != nil {
						return
					}
				} else if handle != nil {
					handle(peer, message)
				}
			}
		}()
		return local, nil
	}})
	t.Cleanup(client.Close)
	return client, requests, dials
}

func nextMessage(t *testing.T, messages <-chan Message) Message {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(2 * time.Second):
		t.Fatal("Missing IPC fixture message")
		return nil
	}
}

func TestConnectCoalescesAndPreservesDirectedWireContract(t *testing.T) {
	client, messages, dials := fixture(t, time.Second, func(peer net.Conn, m Message) {
		if text(m, "type") == "request" {
			_ = success(peer, m, m["params"])
		}
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Connect(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if dials.Load() != 1 || client.ConnectionStatus().State != "connected" {
		t.Fatal("Connect did not coalesce")
	}
	init := nextMessage(t, messages)
	if text(init, "sourceClientId") != "filo" || init["version"] != float64(0) {
		t.Fatalf("initialize envelope: %#v", init)
	}
	reply, err := client.Request(context.Background(), "thread-start", 3, map[string]any{"conversationId": "task"}, "owner")
	if err != nil || text(reply, "handledByClientId") != "owner" {
		t.Fatalf("response: %#v %v", reply, err)
	}
	request := nextMessage(t, messages)
	if text(request, "sourceClientId") != "filo-client" || text(request, "targetClientId") != "owner" || request["version"] != float64(3) || request["timeoutMs"] != float64(1000) {
		t.Fatalf("directed envelope: %#v", request)
	}
	if err := client.Broadcast("following", 1, map[string]any{"following": true}, []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	broadcast := nextMessage(t, messages)
	if text(broadcast, "type") != "broadcast" || text(broadcast, "sourceClientId") != "filo-client" {
		t.Fatal(broadcast)
	}
	targets, _ := broadcast["targetClientIds"].([]any)
	if len(targets) != 1 {
		t.Fatal("Missing broadcast target")
	}
}

func TestFollowerRefusesDiscoveryAndReceivesBroadcast(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	broadcasts := make(chan Message, 1)
	errors := make(chan error, 1)
	client := NewClient(Options{Timeout: time.Second, Dial: func(context.Context) (io.ReadWriteCloser, error) { return local, nil },
		OnBroadcast: func(m Message) { broadcasts <- m }})
	defer client.Close()
	go func() {
		value, err := ReadFrame(peer)
		if err != nil {
			errors <- err
			return
		}
		init, _ := nativejson.Fields(value)
		err = WriteFrame(peer, Message{"type": "client-discovery-request", "requestId": "discovery"})
		if err != nil {
			errors <- err
			return
		}
		value, err = ReadFrame(peer)
		if err != nil {
			errors <- err
			return
		}
		reply, _ := nativejson.Fields(value)
		response, _ := nativejson.Fields(reply["response"])
		if text(reply, "type") != "client-discovery-response" || text(reply, "requestId") != "discovery" || response["canHandle"] != false {
			errors <- io.ErrUnexpectedEOF
			return
		}
		if err = success(peer, init, map[string]any{"clientId": "follower"}); err == nil {
			err = WriteFrame(peer, Message{"type": "broadcast", "method": "changed", "params": map[string]any{"text": "answer"}})
		}
		errors <- err
	}()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if text(nextMessage(t, broadcasts), "method") != "changed" {
		t.Fatal("Missing broadcast")
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
}

func TestResponseTimeoutDoesNotReplayOrCloseHealthyConnection(t *testing.T) {
	client, messages, dials := fixture(t, 80*time.Millisecond, func(peer net.Conn, m Message) {
		if text(m, "method") == "after" {
			_ = success(peer, m, nil)
		}
	})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Request(context.Background(), "slow", 1, nil, "owner"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := client.Request(context.Background(), "after", 1, nil, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"initialize", "slow", "after"} {
		if text(nextMessage(t, messages), "method") != method {
			t.Fatal("Input was replayed or reordered")
		}
	}
	if len(messages) != 0 || dials.Load() != 1 {
		t.Fatal("Timed out request was retried")
	}
}

func TestDisconnectRejectsPendingAndReconnectOnlySendsNewRequest(t *testing.T) {
	var pending atomic.Int32
	client, messages, dials := fixture(t, time.Second, func(peer net.Conn, m Message) {
		if text(m, "method") == "pending" {
			if pending.Add(1) == 2 {
				_ = peer.Close()
			}
		} else {
			_ = success(peer, m, nil)
		}
	})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Request(context.Background(), "pending", 1, nil, "owner"); err == nil {
				t.Error("Disconnected request succeeded")
			}
		}()
	}
	wg.Wait()
	if _, err := client.Request(context.Background(), "after", 1, nil, "owner"); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 2 || pending.Load() != 2 || len(messages) != 5 {
		t.Fatal("Unexpected reconnect/replay count", dials.Load(), pending.Load(), len(messages))
	}
}

func TestConnectFailureIsCoalescedBackedOffAndTerminalCloseDoesNotDial(t *testing.T) {
	var validation, dials atomic.Int32
	failure := errors.New("identity rejected")
	client := NewClient(Options{BeforeConnect: func(context.Context) error { validation.Add(1); return failure },
		Dial: func(context.Context) (io.ReadWriteCloser, error) { dials.Add(1); return nil, failure }})
	for range 3 {
		if err := client.Connect(context.Background()); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	if validation.Load() != 1 || dials.Load() != 0 || client.ConnectionStatus().State != "unavailable" {
		t.Fatal("Identity failure bypassed")
	}
	client.Close()
	if err := client.Connect(context.Background()); err == nil || validation.Load() != 1 {
		t.Fatal("Closed follower reconnected")
	}
}
