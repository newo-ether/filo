package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type hostCall struct {
	method string
	params map[string]any
}

type stubHost struct {
	calls   []hostCall
	handler func(c hostCall) (any, error)
}

func (s *stubHost) Request(method string, params any) (any, error) {
	call := hostCall{method: method}
	// Serialize through JSON like the real transport so map[string]any params
	// reach the stub exactly as a native peer would see them.
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &call.params); err != nil {
		return nil, err
	}
	s.calls = append(s.calls, call)
	return s.handler(call)
}

func idleThread(_ hostCall) (any, error) {
	return map[string]any{"thread": map[string]any{"status": map[string]any{"type": "idle"}}}, nil
}

func stubWith(t *testing.T, handler func(hostCall) (any, error)) *stubHost {
	t.Helper()
	return &stubHost{handler: handler}
}

func TestVerifyHostIdleSinglePagePassesAndCounts(t *testing.T) {
	host := stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			return map[string]any{"data": []any{"a", "b"}, "nextCursor": nil}, nil
		}
		return idleThread(c)
	})
	count, err := VerifyHostIdle(host)
	if err != nil || count != 2 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
	if len(host.calls) != 3 || host.calls[1].params["threadId"] != "a" || host.calls[2].params["threadId"] != "b" {
		t.Fatalf("calls = %+v", host.calls)
	}
	// TS sends { cursor: undefined, limit: 100 } on the first page, which
	// JSON.stringify reduces to { limit: 100 }.
	if _, exists := host.calls[0].params["cursor"]; exists {
		t.Fatalf("first page leaked a cursor key: %+v", host.calls[0].params)
	}
	if limit, _ := host.calls[0].params["limit"].(float64); limit != 100 {
		t.Fatalf("limit = %v", host.calls[0].params["limit"])
	}
	// includeTurns must stay false so the read stays a status probe.
	if turns, _ := host.calls[1].params["includeTurns"].(bool); turns {
		t.Fatalf("includeTurns = %v", host.calls[1].params["includeTurns"])
	}
}

func TestVerifyHostIdleFollowsCursorAndSendsIt(t *testing.T) {
	pages := 0
	host := stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			pages++
			if pages == 1 {
				return map[string]any{"data": []any{"a"}, "nextCursor": "c1"}, nil
			}
			return map[string]any{"data": []any{"b"}, "nextCursor": nil}, nil
		}
		return idleThread(c)
	})
	count, err := VerifyHostIdle(host)
	if err != nil || count != 2 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
	second := host.calls[2]
	if second.method != "thread/loaded/list" || second.params["cursor"] != "c1" {
		t.Fatalf("follow-up page params = %+v", second.params)
	}
}

func TestVerifyHostIdleEmptyStringCursorEndsScan(t *testing.T) {
	// TS: cursor = page.nextCursor || undefined turns "" into undefined, so
	// the scan ends without sending an empty cursor request.
	host := stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			return map[string]any{"data": []any{"a"}, "nextCursor": ""}, nil
		}
		return idleThread(c)
	})
	count, err := VerifyHostIdle(host)
	if err != nil || count != 1 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
	if len(host.calls) != 2 {
		t.Fatalf("calls = %+v", host.calls)
	}
}

func TestVerifyHostIdleRejectsRepeatingCursor(t *testing.T) {
	// The id-duplicate guard fires before the cursor guard (same order as
	// TS), so the repeating page must carry fresh ids to reach the check.
	pages := 0
	host := stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			pages++
			if pages == 1 {
				return map[string]any{"data": []any{"a"}, "nextCursor": "c1"}, nil
			}
			return map[string]any{"data": []any{"b"}, "nextCursor": "c1"}, nil
		}
		return idleThread(c)
	})
	_, err := VerifyHostIdle(host)
	if err == nil || err.Error() != "Auxiliary cursor did not advance" {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyHostIdleRejectsNonStringCursor(t *testing.T) {
	host := stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			return map[string]any{"data": []any{}, "nextCursor": 7}, nil
		}
		return idleThread(c)
	})
	_, err := VerifyHostIdle(host)
	if err == nil || err.Error() != "Auxiliary cursor did not advance" {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyHostIdleNonArrayDataFailsClosed(t *testing.T) {
	for _, page := range []map[string]any{
		{},
		{"data": "nope"},
		{"data": nil},
	} {
		host := stubWith(t, func(c hostCall) (any, error) {
			if c.method == "thread/loaded/list" {
				return page, nil
			}
			return idleThread(c)
		})
		_, err := VerifyHostIdle(host)
		if err == nil || err.Error() != "Cannot verify auxiliary native task state" {
			t.Fatalf("page %v err = %v", page, err)
		}
	}
}

func TestVerifyHostIdleRejectsNonStringAndDuplicateIds(t *testing.T) {
	host := stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			return map[string]any{"data": []any{"a", 5}, "nextCursor": nil}, nil
		}
		return idleThread(c)
	})
	_, err := VerifyHostIdle(host)
	if err == nil || err.Error() != "Invalid auxiliary task catalog" {
		t.Fatalf("err = %v", err)
	}
	host = stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			return map[string]any{"data": []any{"a", "a"}, "nextCursor": nil}, nil
		}
		return idleThread(c)
	})
	_, err = VerifyHostIdle(host)
	if err == nil || err.Error() != "Invalid auxiliary task catalog" {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyHostIdleCapsCatalogAtTenThousand(t *testing.T) {
	ids := make([]any, 0, 10001)
	for i := 0; i < 10001; i++ {
		ids = append(ids, fmt.Sprintf("thread-%05d", i))
	}
	host := stubWith(t, func(c hostCall) (any, error) {
		if c.method == "thread/loaded/list" {
			return map[string]any{"data": ids, "nextCursor": nil}, nil
		}
		return idleThread(c)
	})
	_, err := VerifyHostIdle(host)
	if err == nil || err.Error() != "Invalid auxiliary task catalog" {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyHostIdleActiveThreadYieldsRpcErrorMinus32600(t *testing.T) {
	for _, thread := range []any{
		map[string]any{"status": map[string]any{"type": "active"}},
		map[string]any{"status": map[string]any{"type": "idle-ish"}},
		map[string]any{},
		map[string]any{"status": "weird"},
		// TS thread?.status?.type on a missing thread still takes the active branch.
		nil,
	} {
		host := stubWith(t, func(c hostCall) (any, error) {
			if c.method == "thread/loaded/list" {
				return map[string]any{"data": []any{"a"}, "nextCursor": nil}, nil
			}
			return map[string]any{"thread": thread}, nil
		})
		_, err := VerifyHostIdle(host)
		var rpcError *RpcError
		if !errors.As(err, &rpcError) || rpcError.Message !=
			"A Filo-owned auxiliary task is active; finish it before updating" ||
			rpcError.Code == nil || *rpcError.Code != -32600 {
			t.Fatalf("thread %v err = %v", thread, err)
		}
		if !strings.Contains(err.Error(), "auxiliary task is active") {
			t.Fatalf("message text changed: %v", err)
		}
	}
}

func TestVerifyHostIdlePropagatesTransportErrors(t *testing.T) {
	sentinel := errors.New("transport exploded")
	host := stubWith(t, func(c hostCall) (any, error) {
		return nil, sentinel
	})
	_, err := VerifyHostIdle(host)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
}
