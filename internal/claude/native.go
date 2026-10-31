package claude

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

// Execution turn IDs are Filo correlation IDs, never native Claude turn IDs.
// Native acceptance is separately proven by a replayed user UUID in InputReceipts.
const ExecutionTurnPrefix = "filo.claude.execution."

type NativeOptions struct {
	Executable      string
	Workspace       string
	UploadDirectory string
	Scope           string
	OnChange        func(string)
}

type nativeControl struct {
	done chan struct{}
	err  error
	data map[string]json.RawMessage
}

type nativeInput struct {
	client   string
	text     string
	turn     string
	uuid     string
	done     chan struct{}
	terminal chan struct{}
	resultID string
	failure  error
}

// NativeSession owns exactly one dedicated stream-json process. Request context
// cancellation never closes its pipes or replays an uncertain input.
type NativeSession struct {
	mu          sync.Mutex
	writeMu     sync.Mutex
	input       io.WriteCloser
	command     *exec.Cmd
	options     NativeOptions
	id          string
	binding     string
	model       string
	effort      string // last effort confirmed by get_settings readback
	title       string // last title acknowledged by rename_session
	models      []protocol.Model
	rows        []protocol.Message
	receipts    []protocol.InputReceipt
	inputs      map[string]*nativeInput
	active      *nativeInput
	controlling bool
	fenced      bool
	completed   string
	controls    map[string]*nativeControl
	exited      bool
	updated     time.Time
	rowBytes    int
	stream      *nativeStreamMessage
}

func nativeID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic("native identity entropy unavailable")
	}
	data[6], data[8] = data[6]&15|64, data[8]&63|128
	text := hex.EncodeToString(data[:])
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

func StartNative(ctx context.Context, options NativeOptions) (*NativeSession, error) {
	if !filepath.IsAbs(options.Workspace) || !opaque.MatchString(options.Scope) {
		return nil, errors.New("Invalid dedicated Claude workspace or scope")
	}
	resolved, err := ResolveNativeExecutable(options.Executable)
	if err != nil {
		return nil, err
	}
	args, err := nativeArguments(options)
	if err != nil {
		return nil, err
	}
	command := exec.Command(resolved, args...)
	command.Dir = options.Workspace
	command.SysProcAttr = nativeProcessAttributes()
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		input.Close()
		return nil, err
	}
	s := &NativeSession{input: input, command: command, options: options, binding: nativeID(),
		inputs: map[string]*nativeInput{}, controls: map[string]*nativeControl{}, updated: time.Now()}
	go s.consume(output)
	response, err := s.control(ctx, map[string]any{"subtype": "initialize"})
	if err != nil {
		input.Close()
		return nil, err
	}
	var models []struct {
		Value                 string   `json:"value"`
		DisplayName           string   `json:"displayName"`
		SupportsEffort        bool     `json:"supportsEffort"`
		SupportedEffortLevels []string `json:"supportedEffortLevels"`
	}
	if err := json.Unmarshal(response["models"], &models); err != nil || len(models) > 128 {
		input.Close()
		return nil, errors.New("Claude native model catalog is invalid")
	}
	seen := map[string]bool{}
	for _, model := range models {
		if validNativeModel(model.Value) && !seen[model.Value] {
			seen[model.Value] = true
			entry := protocol.Model{ID: model.Value, Name: protocol.Text(model.DisplayName)}
			if model.SupportsEffort {
				entry.ReasoningEfforts = validEffortLevels(model.SupportedEffortLevels)
			}
			s.models = append(s.models, entry)
		}
	}
	return s, nil
}

func nativeArguments(options NativeOptions) ([]string, error) {
	args := []string{"--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json",
		"--replay-user-messages", "--include-partial-messages", "--permission-mode", "default"}
	if options.UploadDirectory != "" {
		if !filepath.IsAbs(options.UploadDirectory) || strings.ContainsAny(options.UploadDirectory, "\x00\r\n") {
			return nil, errors.New("Invalid Claude upload directory")
		}
		args = append(args, "--add-dir", filepath.Clean(options.UploadDirectory))
	}
	return args, nil
}

func ResolveNativeExecutable(path string) (string, error) {
	if path == "" {
		path = "claude"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", errors.New("Claude executable is unavailable")
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(resolved), ".exe") {
		return "", errors.New("Claude requires a native executable; command wrappers are unsupported")
	}
	return resolved, nil
}

func validNativeModel(model string) bool {
	return len(model) > 0 && len(model) <= 256 && strings.TrimSpace(model) == model && !strings.ContainsAny(model, "\x00\r\n\t")
}

func (s *NativeSession) write(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return json.NewEncoder(s.input).Encode(value)
}

func (s *NativeSession) signalLocked() { s.updated = time.Now() }

func (s *NativeSession) fence() {
	s.mu.Lock()
	s.fenced = true
	s.model = ""
	s.signalLocked()
	s.mu.Unlock()
}

func (s *NativeSession) consume(output io.ReadCloser) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var event map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &event) != nil || s.handle(event) != nil {
			s.fence()
			// Drain without closing either pipe: malformed observation must not
			// terminate an accepted native generation.
			_, _ = io.Copy(io.Discard, output)
			break
		}
	}
	if scanner.Err() != nil {
		s.fence()
		_, _ = io.Copy(io.Discard, output)
	}
	output.Close()
	s.mu.Lock()
	s.exited = true
	for _, c := range s.controls {
		c.err = errors.New("Claude native connection ended")
		close(c.done)
	}
	s.controls = map[string]*nativeControl{}
	if s.active != nil && s.active.uuid == "" {
		s.active.failure = errors.New("Claude input acceptance is unknown")
		close(s.active.done)
	}
	s.signalLocked()
	s.mu.Unlock()
	s.input.Close()
	_ = s.command.Wait()
	s.notify()
}

func stringField(event map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(event[key], &value)
	return value
}

func (s *NativeSession) control(ctx context.Context, request map[string]any) (map[string]json.RawMessage, error) {
	id := nativeID()
	c := &nativeControl{done: make(chan struct{})}
	s.mu.Lock()
	if s.exited || s.fenced || len(s.controls) >= 8 {
		s.mu.Unlock()
		return nil, errors.New("Claude control is unavailable")
	}
	s.controls[id] = c
	s.mu.Unlock()
	if err := s.write(map[string]any{"type": "control_request", "request_id": id, "request": request}); err != nil {
		s.fence()
		return nil, errors.New("Claude control delivery is unknown")
	}
	select {
	case <-ctx.Done():
		s.fence()
		return nil, ctx.Err()
	case <-c.done:
		return c.data, c.err
	}
}

func (s *NativeSession) notify() {
	if s.options.OnChange != nil {
		s.mu.Lock()
		id := s.id
		s.mu.Unlock()
		if id != "" {
			s.options.OnChange(id)
		}
	}
}

func (s *NativeSession) Models() []protocol.Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Model{}, s.models...)
}
func (s *NativeSession) ID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.id }
func (s *NativeSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.exited && !s.fenced
}

// CloseIdle closes only a process that never admitted an input.
func (s *NativeSession) CloseIdle() {
	s.mu.Lock()
	idle := len(s.inputs) == 0
	s.mu.Unlock()
	if idle {
		s.input.Close()
	}
}
