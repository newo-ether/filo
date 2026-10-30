package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

const cliSharedRecordName = "shared.json"

// A record is only a rendezvous. Online requires both the launcher's live lease
// and an authenticated native handshake, with the selected native home.
type cliSharedRecord struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	Lease     string `json:"lease"`
	Home      string `json:"home"`
	Workspace string `json:"workspace"`
}

func cliSharedRoot() (string, error) {
	profile, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(profile, ".filo", "codex-shared"), nil
}

func readCLISharedRecord(directory string) (cliSharedRecord, error) {
	value, err := ReadExecutorFile(directory, cliSharedRecordName)
	if err != nil {
		return cliSharedRecord{}, err
	}
	body, err := json.Marshal(value)
	var record cliSharedRecord
	if err == nil {
		err = json.Unmarshal(body, &record)
	}
	if err != nil || !endpointPattern.MatchString(record.URL) || !tokenPattern.MatchString(record.Token) ||
		!validCLISharedLeaseURL(record.Lease) || !absoluteWorkspace(record.Home) || !absoluteWorkspace(record.Workspace) {
		return cliSharedRecord{}, errors.New("Invalid shared CLI rendezvous")
	}
	return record, nil
}

func validCLISharedLeaseURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil ||
		parsed.Path != "/lease" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port <= 65535
}

func cliSharedLease(ctx context.Context, record cliSharedRecord) bool {
	if !validCLISharedLeaseURL(record.Lease) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, record.Lease, nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+record.Token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusNoContent
}

type cliSharedFactory struct {
	daemon    func(context.Context) (*codex.CodexHost, error)
	root      string
	home      func(context.Context) (string, error)
	mu        sync.Mutex
	catalogMu sync.Mutex
	tasks     map[string]cliSharedRecord
}

func (f *cliSharedFactory) Owns(id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.tasks[id]
	return ok, nil
}
func (f *cliSharedFactory) Close() {}
func (f *cliSharedFactory) Open(ctx context.Context, id *string) (TaskSessionsConnection, error) {
	if id == nil {
		return nil, errors.New("Shared CLI creation belongs to the terminal")
	}
	return f.Follow(ctx, *id)
}
func (f *cliSharedFactory) Follow(ctx context.Context, id string) (TaskSessionsConnection, error) {
	if f.daemon != nil {
		return f.followDaemon(ctx, id)
	}
	f.mu.Lock()
	record, ok := f.tasks[id]
	f.mu.Unlock()
	if !ok || !cliSharedLease(ctx, record) {
		return nil, codexcliUnavailable()
	}
	host, err := f.connect(ctx, record)
	if err != nil {
		return nil, err
	}
	ids, err := cliSharedLoaded(host)
	if err != nil || !containsTurn(ids, id) {
		host.Close()
		return nil, codexcliUnavailable()
	}
	return &cliSharedConnection{host: host, state: &codex.NativeLiveState{Rpc: host.Rpc, ID: id}, record: record}, nil
}
func codexcliUnavailable() error { return errors.New("Shared native CLI connection is unavailable") }

func (f *cliSharedFactory) connect(ctx context.Context, record cliSharedRecord) (*codex.CodexHost, error) {
	home, err := f.home(ctx)
	if err != nil || !strings.EqualFold(filepath.Clean(home), filepath.Clean(record.Home)) {
		return nil, codexcliUnavailable()
	}
	host, err := codex.ConnectCodexHost(ctx, record.URL, codex.HostOptions{Token: &record.Token, Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Clean(home), filepath.Clean(host.CodexHome)) {
		host.Close()
		return nil, codexcliUnavailable()
	}
	return host, nil
}

func cliSharedLoaded(host *codex.CodexHost) ([]string, error) {
	value, err := host.Rpc.RequestTimeout("thread/loaded/list", map[string]any{}, 2*time.Second)
	if err != nil {
		return nil, err
	}
	fields, _ := nativejson.Fields(value)
	values, ok := fields["data"].([]any)
	if !ok || len(values) > 30 || fields["nextCursor"] != nil {
		return nil, errors.New("Invalid or excessive loaded CLI threads")
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id := nativejson.Text(value)
		if !taskIDPattern.MatchString(id) {
			return nil, errors.New("Invalid loaded CLI identity")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *cliSharedFactory) catalog(ctx context.Context) (rows []protocol.Session, catalogErr error) {
	f.catalogMu.Lock()
	defer f.catalogMu.Unlock()
	// Publish completed discovery atomically. An in-flight refresh must not
	// route an admitted shared task through another access path. Failed or
	// absent discovery still discards all earlier admission evidence.
	defer func() {
		if catalogErr != nil || len(rows) == 0 {
			f.mu.Lock()
			f.tasks = map[string]cliSharedRecord{}
			f.mu.Unlock()
		}
	}()
	if f.daemon != nil {
		return f.daemonCatalog(ctx)
	}
	directory, err := os.Open(f.root)
	if errors.Is(err, os.ErrNotExist) {
		return []protocol.Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(129)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 128 {
		return nil, errors.New("Shared CLI registry capacity exceeded")
	}
	result := []protocol.Session{}
	tasks := map[string]cliSharedRecord{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !uuidPattern.MatchString(entry.Name()) {
			continue
		}
		record, err := readCLISharedRecord(filepath.Join(f.root, entry.Name()))
		if err != nil || !cliSharedLease(ctx, record) {
			continue
		}
		host, err := f.connect(ctx, record)
		if err != nil {
			continue
		}
		ids, readErr := cliSharedLoaded(host)
		if readErr == nil {
			for _, id := range ids {
				value, err := host.Rpc.RequestTimeout("thread/read", map[string]any{"threadId": id, "includeTurns": false}, 2*time.Second)
				if err != nil {
					continue
				}
				fields, _ := nativejson.Fields(value)
				thread, _ := nativejson.Fields(fields["thread"])
				if nativejson.Text(thread["id"]) != id || thread["parentThreadId"] != nil {
					continue
				}
				if _, duplicate := tasks[id]; duplicate {
					continue
				}
				row, err := cliLiveSession(value)
				if err != nil {
					continue
				}
				tasks[id] = record
				result = append(result, row)
			}
		}
		host.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.tasks = tasks
	f.mu.Unlock()
	return result, nil
}

func cliLiveSession(value any) (protocol.Session, error) {
	fields, _ := nativejson.Fields(value)
	thread, _ := nativejson.Fields(fields["thread"])
	id := nativejson.Text(thread["id"])
	if !taskIDPattern.MatchString(id) {
		return protocol.Session{}, errors.New("Invalid native thread identity")
	}
	title := nativejson.Text(thread["name"])
	if title == "" {
		title = nativejson.Text(thread["preview"])
	}
	if title == "" {
		title = id
	}
	status, _ := nativejson.Fields(thread["status"])
	state := nativejson.Text(status["type"])
	if state == "" || state == "notLoaded" {
		return protocol.Session{}, errors.New("Native task is no longer loaded")
	}
	updated, _ := thread["updatedAt"].(float64)
	return protocol.Session{
		ID: id, Title: protocol.Text(title), Cwd: nativejson.Text(thread["cwd"]),
		UpdatedAt: updated, Status: protocol.Known(&state),
	}, nil
}

type cliSharedConnection struct {
	host   *codex.CodexHost
	state  *codex.NativeLiveState
	record cliSharedRecord
}

func (c *cliSharedConnection) Transport() *codex.Rpc             { return c.host.Rpc }
func (c *cliSharedConnection) Close()                            { c.host.Close() }
func (c *cliSharedConnection) Observe(method string, params any) { c.state.Receive(method, params) }
func (c *cliSharedConnection) TaskState(id string) (any, error) {
	if !cliSharedLease(context.Background(), c.record) {
		return nil, codexcliUnavailable()
	}
	return c.state.State(id)
}
