package service

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
)

// The system-service and standalone Go entry points share the private directory
// layout, bind validation and shutdown latch defined here.
const (
	// entryConfigName is the private configuration record of one entry directory.
	entryConfigName = "config.json"
	// entryTokenName is the private bearer credential of one entry point.
	entryTokenName = "token"
	// serviceTaskDirectoryName is the executor root of one user helper, resolved
	// relative to the helper credential itself.
	serviceTaskDirectoryName = "task-executors"
	// entryHeaderTimeout bounds incomplete headers before authentication.
	// Streaming response bodies use their own cancellation and frame bounds.
	entryHeaderTimeout = 5 * time.Second
)

// entryBindPattern admits all-interface IPv4, loopback and the RFC 6598 range.
// Private workers independently require loopback in their entry-point validation.
var entryBindPattern = regexp.MustCompile(`^(0\.0\.0\.0|127\.0\.0\.1|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3})$`)

// isEntryIPv4 requires four decimal octets without leading zeroes. Abbreviated
// addresses such as `127.1` and padded octets such as `100.064.1.1` are refused
// before applying the listener address allowlist.
func isEntryIPv4(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 3 {
			return false
		}
		for index := 0; index < len(part); index++ {
			if part[index] < '0' || part[index] > '9' {
				return false
			}
		}
		if len(part) > 1 && part[0] == '0' {
			return false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number > 255 {
			return false
		}
	}
	return true
}

// readEntryBinding admits the private bind/port pair of one entry configuration.
// The JSON port must be an integer between 1024 and 65535.
func readEntryBinding(config map[string]any) (string, int, bool) {
	bind, isText := config["bind"].(string)
	if !isText || !isEntryIPv4(bind) || !entryBindPattern.MatchString(bind) {
		return "", 0, false
	}
	port, isNumber := config["port"].(float64)
	if !isNumber || port != math.Trunc(port) || port < 1024 || port > 65535 {
		return "", 0, false
	}
	return bind, int(port), true
}

// readEntryJSON reads one private JSON record. A non-object document yields an
// empty record so the caller rejects its missing configuration fields.
func readEntryJSON(path string) (map[string]any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	if record, isObject := value.(map[string]any); isObject {
		return record, nil
	}
	return map[string]any{}, nil
}

// readEntryText reads one private credential and trims ECMAScript whitespace,
// including the byte order mark that strings.TrimSpace leaves intact.
func readEntryText(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return codex.TrimJSWhitespace(string(body)), nil
}

// writeEntryRecord prints one entry point record as a single JSON line, which is
// how a launcher observes a ready gateway. Nil selects standard output.
func writeEntryRecord(output io.Writer, record any) {
	if output == nil {
		output = os.Stdout
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = output.Write(append(encoded, '\n'))
}

// writeEntryDiagnostic writes one startup failure line for the service manager.
func writeEntryDiagnostic(output io.Writer, prefix, message string) {
	if output == nil {
		output = os.Stderr
	}
	_, _ = io.WriteString(output, prefix+message+"\n")
}

// entrySignals registers the two termination signals both entry points stop on.
// Cancelling restores the default handlers.
func entrySignals() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals, func() { signal.Stop(signals) }
}

// exitLatch serializes shutdown requests. The first caller runs shutdown inline
// and owns the exit code. Reentrant calls, including native close handlers invoked
// during shutdown, return immediately to avoid waiting on themselves.
type exitLatch struct {
	mu      sync.Mutex
	stopped bool
	code    int
	done    chan struct{}
}

// newExitLatch prepares one latch.
func newExitLatch() *exitLatch { return &exitLatch{done: make(chan struct{})} }

// stop runs the shutdown exactly once. Every later caller reports that the exit
// code is already owned, so a second signal never runs a second shutdown.
func (latch *exitLatch) stop(code int, shutdown func()) {
	latch.mu.Lock()
	if latch.stopped {
		latch.mu.Unlock()
		return
	}
	latch.stopped = true
	latch.code = code
	latch.mu.Unlock()
	defer close(latch.done)
	shutdown()
}

// stopping reports whether some caller already claimed the shutdown. An entry
// point checks it before and after native startup, because a cancellation can
// arrive while the native connection is still opening.
func (latch *exitLatch) stopping() bool {
	latch.mu.Lock()
	defer latch.mu.Unlock()
	return latch.stopped
}

// exitCode returns the code of the claiming caller.
func (latch *exitLatch) exitCode() int {
	latch.mu.Lock()
	defer latch.mu.Unlock()
	return latch.code
}

// wait blocks until the claimed shutdown has returned and reports its code.
func (latch *exitLatch) wait() int {
	<-latch.done
	return latch.exitCode()
}

// createdScope exposes the separately verified Filo-created tasks of one leased
// user helper through the desktop session surface. Being present in ordinary
// native history never grants this scope, and the scope never creates, resumes
// or executes a task of its own.
type createdScope struct {
	tasks *CreatedTaskClient
}

// Owns reports whether one identifier belongs to this private scope.
func (scope createdScope) Owns(_ context.Context, id string) (bool, error) {
	return scope.tasks.Owns(id)
}

// Read returns one page of a created task. An empty cursor requests the initial
// page by omitting the cursor from the client request.
func (scope createdScope) Read(ctx context.Context, id, cursor string, activity bool) (protocol.ConversationPage, error) {
	var after *string
	if cursor != "" {
		after = &cursor
	}
	page, err := scope.tasks.Read(ctx, id, after, activity)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	return *page, nil
}

// Statuses reports the status of the listed created tasks only.
func (scope createdScope) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	return scope.tasks.Statuses(ctx, ids)
}

// Attach subscribes to one created task and returns the detach action that
// releases exactly this subscription.
func (scope createdScope) Attach(ctx context.Context, id string) (func(), error) {
	if err := scope.tasks.Attach(ctx, id); err != nil {
		return nil, err
	}
	return func() { scope.tasks.Detach(id) }, nil
}

// Send submits one message to a created task.
func (scope createdScope) Send(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	return scope.tasks.Send(ctx, id, text, clientID)
}

// Stop stops one turn of a created task.
func (scope createdScope) Stop(ctx context.Context, id, turnID string) error {
	return scope.tasks.Stop(ctx, id, turnID)
}

// UpdateSettings applies one settings document to a created task.
func (scope createdScope) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	return scope.tasks.UpdateSettings(ctx, id, settings)
}

var _ sessions.CreatedReader = createdScope{}
