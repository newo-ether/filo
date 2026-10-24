package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

type cliMetadataFixture struct {
	mu       sync.Mutex
	methods  []string
	released int
	home     string
}

func newCLIMetadataFixture(t *testing.T) (*cliSourceMetadata, *cliMetadataFixture) {
	t.Helper()
	f := &cliMetadataFixture{home: t.TempDir()}
	m := newCLISourceMetadata(func(context.Context) (string, error) { return "scope-a", nil },
		func(_ context.Context, home string) error {
			if home != f.home {
				return errors.New("wrong store")
			}
			return nil
		})
	m.open = func(context.Context) (cliMetadataConnection, error) {
		client, server := net.Pipe()
		rpc := codex.NewRpc(client, client)
		go func() {
			defer server.Close()
			scanner := bufio.NewScanner(server)
			encoder := json.NewEncoder(server)
			for scanner.Scan() {
				var packet struct {
					ID     *int   `json:"id"`
					Method string `json:"method"`
				}
				if json.Unmarshal(scanner.Bytes(), &packet) != nil {
					return
				}
				f.mu.Lock()
				f.methods = append(f.methods, packet.Method)
				f.mu.Unlock()
				if packet.ID == nil {
					continue
				}
				var result any = map[string]any{}
				switch packet.Method {
				case "initialize":
					result = map[string]any{"codexHome": f.home}
				case "model/list":
					result = map[string]any{"data": []any{map[string]any{"model": "native-model", "displayName": "Native model"}}}
				case "account/rateLimits/read":
					result = map[string]any{"rateLimits": map[string]any{"limitName": "Native account"}}
				}
				if encoder.Encode(map[string]any{"id": *packet.ID, "result": result}) != nil {
					return
				}
			}
		}()
		return cliMetadataConnection{rpc: rpc, release: func() {
			rpc.Close()
			f.mu.Lock()
			f.released++
			f.mu.Unlock()
		}}, nil
	}
	t.Cleanup(m.Close)
	return m, f
}

func TestCLISourceMetadataReadsWithoutTaskOperations(t *testing.T) {
	m, f := newCLIMetadataFixture(t)
	models, err := m.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "native-model" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	usage, err := m.Usage(context.Background())
	if err != nil || len(usage.Limits) != 1 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"initialize", "initialized", "model/list", "initialize", "initialized", "account/rateLimits/read"}
	if !reflect.DeepEqual(f.methods, want) || f.released != 2 {
		t.Fatalf("methods=%v released=%d", f.methods, f.released)
	}
}

func TestCLISourceMetadataRefusesIdentityBeforeMetadata(t *testing.T) {
	m, f := newCLIMetadataFixture(t)
	m.verify = func(context.Context, string) error { return errors.New("identity refused") }
	if _, err := m.Models(context.Background()); err == nil {
		t.Fatal("identity accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.methods, []string{"initialize"}) || f.released != 1 {
		t.Fatalf("methods=%v releases=%d", f.methods, f.released)
	}
}

func TestCLISourceMetadataDiscardsChangedScope(t *testing.T) {
	m, _ := newCLIMetadataFixture(t)
	reads := 0
	m.scope = func(context.Context) (string, error) {
		reads++
		if reads == 1 {
			return "before", nil
		}
		return "after", nil
	}
	if models, err := m.Models(context.Background()); err == nil || models != nil {
		t.Fatal("old-scope metadata escaped")
	}
}

func TestCLISourceMetadataCancellationCloseAndAllowedMethods(t *testing.T) {
	m, _ := newCLIMetadataFixture(t)
	opened := false
	m.open = func(ctx context.Context) (cliMetadataConnection, error) {
		opened = true
		<-ctx.Done()
		return cliMetadataConnection{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.Models(ctx); !errors.Is(err, context.DeadlineExceeded) || !opened {
		t.Fatalf("cancel err=%v open=%t", err, opened)
	}
	m.Close()
	opened = false
	if _, err := m.Usage(context.Background()); err == nil || opened {
		t.Fatal("closed metadata opened a process")
	}
	if _, err := (cliMetadataRPC{}).Request("thread/start", nil); err == nil {
		t.Fatal("metadata admitted task")
	}
}

func TestCLISourceMetadataCloseCancelsActiveRead(t *testing.T) {
	m, _ := newCLIMetadataFixture(t)
	started := make(chan struct{})
	m.open = func(ctx context.Context) (cliMetadataConnection, error) {
		close(started)
		<-ctx.Done()
		return cliMetadataConnection{}, ctx.Err()
	}
	result := make(chan error, 1)
	go func() { _, err := m.Models(context.Background()); result <- err }()
	<-started
	m.Close()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active read err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active metadata read")
	}
}

func TestCLIControlledRoutesTaskFreeMetadataWithoutWriter(t *testing.T) {
	m, f := newCLIMetadataFixture(t)
	surface, _ := newCLIControlledFixture(t)
	surface.metadata = m
	caps := surface.Capabilities()
	if !caps.Models || !caps.Usage {
		t.Fatal("metadata routes not advertised")
	}
	if _, err := surface.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := surface.Usage(context.Background()); err != nil {
		t.Fatal(err)
	}
	surface.Close()
	if _, err := m.Models(context.Background()); err == nil {
		t.Fatal("controlled surface left metadata open")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.released != 2 {
		t.Fatalf("metadata connections released=%d", f.released)
	}
}
