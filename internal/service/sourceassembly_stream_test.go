package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func awaitAssemblyStreamCount(t *testing.T, source *userSource, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if source.mu.TryLock() {
			count := len(source.streaming)
			source.mu.Unlock()
			if count == want {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("private source did not reach %d streams; cleanup may retain its mutex", want)
}

func TestAssemblyPrivateStreamReleaseAndReplacementRemainIndependent(t *testing.T) {
	var mu sync.Mutex
	scope := "scope-a"
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/info" {
			mu.Lock()
			current := scope
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"extensions": map[string]int{"multiAdapter": 1},
				"adapters":   []protocol.AdapterDescriptor{{ID: claudeCodeSource, NativeScopeID: current}},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer private.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := newUserWorkerFixture(t, private.URL, StaticUserWorkerToken(testToken),
		userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
	source := newUserSource(worker, claudeCodeSource, nil)
	attach := func() func() {
		t.Helper()
		release, err := source.Attach(ctx, payloadSession)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		return release
	}
	first := attach()
	awaitAssemblyStreamCount(t, source, 1)
	first()
	first()
	awaitAssemblyStreamCount(t, source, 0)

	second := attach()
	third := attach()
	awaitAssemblyStreamCount(t, source, 2)
	second()
	awaitAssemblyStreamCount(t, source, 1)
	first()
	awaitAssemblyStreamCount(t, source, 1)

	mu.Lock()
	scope = "scope-b"
	mu.Unlock()
	if current, err := source.scope(context.Background()); err != nil || current != "scope-b" {
		t.Fatalf("replacement scope = %q, %v", current, err)
	}
	awaitAssemblyStreamCount(t, source, 0)
	fourth := attach()
	awaitAssemblyStreamCount(t, source, 1)
	third()
	awaitAssemblyStreamCount(t, source, 1)
	fourth()
	awaitAssemblyStreamCount(t, source, 0)
}
