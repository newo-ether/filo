package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/desktop"
)

type broadcast struct {
	method  string
	version int
	params  map[string]any
	targets []string
}
type ipcFixture struct {
	mu     sync.Mutex
	f      *Followers
	auto   bool
	owner  string
	err    error
	calls  []broadcast
	closed int
}

func (ipc *ipcFixture) Connect(context.Context) error { return nil }
func (ipc *ipcFixture) Request(ctx context.Context, method string, version int, params any, target string) (desktop.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ipc.mu.Lock()
	defer ipc.mu.Unlock()
	if ipc.err != nil {
		return nil, ipc.err
	}
	if method != "thread-owner-discovery" || version != 1 || target != "" {
		return nil, errors.New("Unexpected native operation")
	}
	if ipc.owner == "" {
		return desktop.Message{"resultType": "error", "error": "no-client-found"}, nil
	}
	return desktop.Message{"resultType": "success", "handledByClientId": ipc.owner, "result": map[string]any{"supportsUntrustedAppInput": true}}, nil
}
func snapshotMessage(id, owner string, revision float64) desktop.Message {
	return desktop.Message{"method": "thread-stream-state-changed", "version": float64(11), "sourceClientId": owner, "params": map[string]any{"hostId": "local", "conversationId": id, "change": map[string]any{"type": "snapshot", "revision": revision, "conversationState": map[string]any{"id": id, "hostId": "local", "cwd": "C:/project", "turns": []any{}}}}}
}
func (ipc *ipcFixture) Broadcast(method string, version int, params any, targets []string) error {
	ipc.mu.Lock()
	ipc.calls = append(ipc.calls, broadcast{method, version, fields(params), targets})
	auto, owner, err := ipc.auto, ipc.owner, ipc.err
	ipc.mu.Unlock()
	if auto && fields(params)["following"] == true && err == nil {
		go ipc.f.Receive(snapshotMessage(text(fields(params)["conversationId"]), owner, 1))
	}
	return err
}
func (ipc *ipcFixture) Close() { ipc.mu.Lock(); ipc.closed++; ipc.mu.Unlock() }
func (ipc *ipcFixture) count(following bool) int {
	ipc.mu.Lock()
	defer ipc.mu.Unlock()
	count := 0
	for _, call := range ipc.calls {
		if call.params["following"] == following {
			count++
		}
	}
	return count
}
func fixture(hooks Hooks) (*Followers, *ipcFixture) {
	ipc := &ipcFixture{auto: true, owner: "original"}
	f := NewFollowers(ipc, hooks)
	ipc.f = f
	return f, ipc
}

func TestSharedFollowerKeepsChatReferenceWhenTransientStatusReleases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := fixture(Hooks{})
		defer f.Close()
		entries := make(chan *followed, 2)
		for i := 0; i < 2; i++ {
			go func() {
				entry, err := f.state(context.Background(), "task", "original", true, true)
				if err != nil {
					panic(err)
				}
				entries <- entry
			}()
		}
		a, b := <-entries, <-entries
		if a != b || ipc.count(true) != 1 {
			t.Fatal("Concurrent readers created different followers")
		}
		f.release(a, true)
		if _, err := f.snapshot(b); err != nil || ipc.count(false) != 0 {
			t.Fatal("Transient reader released open chat", err)
		}
		f.release(b, true)
		if _, err := f.snapshot(b); err == nil || ipc.count(false) != 1 {
			t.Fatal("Last reader did not release follower")
		}
	})
}

func TestSnapshotAbsenceHasShortReadFallbackAndBoundedRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := fixture(Hooks{})
		defer f.Close()
		ipc.auto = false
		start := time.Now()
		entry, err := f.state(context.Background(), "task", "original", true, true)
		if err != nil || entry != nil || time.Since(start) != 2*time.Second {
			t.Fatal("Read did not yield persisted-data opportunity", err)
		}
		time.Sleep(14 * time.Second)
		if ipc.count(false) != 1 {
			t.Fatal("Absent snapshot kept its native subscription")
		}
		if entry, err := f.state(context.Background(), "task", "original", false, true); err != nil || entry != nil || ipc.count(true) != 1 {
			t.Fatal("Snapshot retry backoff was bypassed")
		}
		time.Sleep(30 * time.Second)
		ipc.mu.Lock()
		ipc.auto = true
		ipc.mu.Unlock()
		entry, err = f.state(context.Background(), "task", "original", false, true)
		if err != nil || entry == nil || ipc.count(true) != 2 {
			t.Fatal("Snapshot did not recover", err)
		}
	})
}

func TestLateSnapshotCompletesSameSubscriptionAfterShortReadTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := fixture(Hooks{})
		defer f.Close()
		ipc.auto = false
		if entry, err := f.state(context.Background(), "task", "original", true, true); entry != nil || err != nil {
			t.Fatal(entry, err)
		}
		f.Receive(snapshotMessage("task", "original", 1))
		entry, err := f.state(context.Background(), "task", "original", false, true)
		if err != nil || entry == nil || ipc.count(true) != 1 {
			t.Fatal("Late snapshot created a replacement subscription", err)
		}
		f.detach("task")
		time.Sleep(36 * time.Second)
		if _, err := f.snapshot(entry); err == nil || ipc.count(false) != 1 {
			t.Fatal("Idle follower was not retired")
		}
	})
}

func TestWrongSourceIgnoredAndRevisionGapInvalidatesCurrentState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		closed := 0
		f, ipc := fixture(Hooks{Closed: func() { closed++ }})
		defer f.Close()
		entry, err := f.state(context.Background(), "task", "original", true, false)
		if err != nil {
			t.Fatal(err)
		}
		bad := snapshotMessage("task", "stranger", 100)
		fields(bad["params"])["change"] = map[string]any{"type": "patches", "revision": float64(100), "baseRevision": float64(99), "patches": []any{}}
		f.Receive(bad)
		if closed != 0 {
			t.Fatal("Foreign broadcast changed follower")
		}
		bad["sourceClientId"] = "original"
		f.Receive(bad)
		if _, err := f.snapshot(entry); err == nil || closed != 1 || ipc.count(false) != 1 {
			t.Fatal("Revision gap retained control state")
		}
	})
}

func TestCancellationAndDisconnectCannotBecomeSnapshotFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := fixture(Hooks{})
		defer f.Close()
		ipc.auto = false
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { _, err := f.state(ctx, "task", "original", true, true); result <- err }()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		go func() { _, err := f.state(context.Background(), "task", "original", false, true); result <- err }()
		synctest.Wait()
		f.Disconnected()
		if err := <-result; err == nil {
			t.Fatal("Disconnect was treated as snapshot absence")
		}
		if ipc.count(false) != 0 {
			t.Fatal("Disconnected cleanup contacted a new owner")
		}
	})
}

func TestFollowerCapacityOwnerRecheckAndCloseRemainBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := fixture(Hooks{})
		for _, id := range []string{"a", "b", "c", "d"} {
			owner, err := f.owner(context.Background(), id)
			if err != nil || owner != "original" {
				t.Fatal(owner, err)
			}
			if _, err := f.state(context.Background(), id, owner, true, false); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.state(context.Background(), "e", "original", true, false); err == nil {
			t.Fatal("Follower capacity exceeded")
		}
		ipc.mu.Lock()
		ipc.owner = ""
		ipc.mu.Unlock()
		if owner, err := f.owner(context.Background(), "a"); err != nil || owner != "" {
			t.Fatal("Lost native owner was fabricated")
		}
		f.Close()
		f.Close()
		if ipc.count(false) != 4 || ipc.closed != 1 {
			t.Fatal("Close did not release exactly its own subscriptions")
		}
		if _, err := f.state(context.Background(), "a", "original", true, false); err == nil {
			t.Fatal("Closed follower revived")
		}
	})
}

func TestOwnerReplacementDoesNotTransferOldReferencesOrReleaseNewReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		closed := 0
		f, ipc := fixture(Hooks{Closed: func() { closed++ }})
		defer f.Close()
		old, err := f.state(context.Background(), "task", "original", true, false)
		if err != nil {
			t.Fatal(err)
		}
		ipc.mu.Lock()
		ipc.owner = "new-original"
		ipc.mu.Unlock()
		current, err := f.state(context.Background(), "task", "new-original", true, false)
		if err != nil {
			t.Fatal(err)
		}
		if closed != 1 || current == old {
			t.Fatal("Old owner readers remained attached")
		}
		f.release(old, true)
		if _, err := f.snapshot(current); err != nil {
			t.Fatal("Stale release discarded new owner")
		}
		f.release(current, true)
		if _, err := f.snapshot(current); err == nil || ipc.count(false) != 2 {
			t.Fatal("Old references leaked into new owner")
		}
	})
}

func TestRequiredSnapshotTimeoutRetainsNativeRPCError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := fixture(Hooks{})
		defer f.Close()
		ipc.auto = false
		start := time.Now()
		_, err := f.state(context.Background(), "task", "original", false, false)
		var rpc *codex.RpcError
		if !errors.As(err, &rpc) || rpc.Code == nil || *rpc.Code != -32600 || time.Since(start) != 15*time.Second {
			t.Fatal("Required snapshot lost its native error contract", err)
		}
	})
}

func TestValidPatchesAreImmutableAndCannotChangeThreadIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := fixture(Hooks{})
		defer f.Close()
		entry, err := f.state(context.Background(), "task", "original", true, false)
		if err != nil {
			t.Fatal(err)
		}
		before, err := f.snapshot(entry)
		if err != nil {
			t.Fatal(err)
		}
		patch := snapshotMessage("task", "original", 2)
		fields(patch["params"])["change"] = map[string]any{"type": "patches", "revision": float64(2), "baseRevision": float64(1), "patches": []any{map[string]any{"op": "add", "path": []any{"hasUnreadTurn"}, "value": true}}}
		f.Receive(patch)
		after, err := f.snapshot(entry)
		if err != nil || after["hasUnreadTurn"] != true || before["hasUnreadTurn"] != nil {
			t.Fatal("Patch mutated prior revision", err)
		}
		f.Receive(desktop.Message{"method": "thread-stream-following-status-requested", "version": float64(1), "params": map[string]any{"hostId": "local", "conversationId": "task"}})
		synctest.Wait()
		if ipc.count(true) != 2 {
			t.Fatal("Native following-status request was ignored")
		}
		fields(patch["params"])["change"] = map[string]any{"type": "patches", "revision": float64(3), "baseRevision": float64(2), "patches": []any{map[string]any{"op": "replace", "path": []any{"id"}, "value": "foreign"}}}
		f.Receive(patch)
		if _, err := f.snapshot(entry); err == nil {
			t.Fatal("Patch changed the followed thread identity")
		}
	})
}
