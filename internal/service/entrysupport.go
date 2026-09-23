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

// The two remaining TypeScript entry points, `system-service.ts` and
// `standalone.ts`, are Go functions in this package. They share the private
// layout of one entry directory, the bind admission and the shutdown latch, and
// the shared pieces live here so neither entry point owns a second copy.
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

// entryBindPattern admits exactly the loopback address and the RFC 6598 range the
// TypeScript entry points accept, so a private gateway can never be published
// beyond the host or its tailnet.
var entryBindPattern = regexp.MustCompile(`^(127\.0\.0\.1|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3})$`)

// isEntryIPv4 reproduces the TypeScript check `isIP(value) === 4`, which both
// entry points apply before the bind pattern. Go's net.ParseIP is deliberately
// not used: it also accepts values Node rejects, such as `127.1` and
// `100.064.1.1`, and an entry bind must never be admitted under a looser rule
// than the one that guarded it before.
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
// The port is a JSON number, so it is compared as a float: a fractional or
// out-of-range value is refused exactly where the TypeScript `Number.isInteger`
// range check refuses it.
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

// readEntryJSON reads one private JSON record. A document that is not an object
// yields an empty record, so the caller reports the same admission refusal the
// TypeScript entry point reports for a primitive document, where a primitive has
// no configuration field either.
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

// readEntryText reads one private credential and trims it with the same
// whitespace the TypeScript entry points trim, which is wider than Go's
// strings.TrimSpace.
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

// writeEntryDiagnostic prints the one bounded line an entry point reports when it
// cannot start. The TypeScript entry points let Node print an uncaught stack
// instead; a service manager reads a single line.
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

// exitLatch serializes the first shutdown request of one entry point. The first
// caller runs the shutdown inline and owns the exit code, which is what the
// TypeScript `stopping` flag does. A reentrant call, including the one a native
// close handler makes while shutdown is already closing that native connection,
// returns immediately instead of waiting on the shutdown it interrupted.
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

// Read returns one page of a created task. The empty cursor is passed as an
// absent cursor, which is how the TypeScript client receives it.
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
