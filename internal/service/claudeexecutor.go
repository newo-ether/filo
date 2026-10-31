package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

// ClaudeExecutorOptions belongs to the selected ordinary-user helper. Directory
// is its private Filo execution directory, never a native auth/history directory.
// ReadOnly may supply existing Mod sessions; they never gain writer ownership.
type ClaudeExecutorOptions struct {
	Directory       string
	Workspace       string
	UploadDirectory string
	NativeScopeID   string
	OnChange        func(string)
	ReadOnly        SessionAPI
	Executable      string
	Launch          func(string) error
}

type ClaudeExecutorSessions struct {
	options ClaudeExecutorOptions
	mu      sync.Mutex
	opening sync.Mutex
	closed  bool
	client  *http.Client
	legacy  *ClaudeExecutorSessions
}

func NewClaudeExecutorSessions(options ClaudeExecutorOptions) (*ClaudeExecutorSessions, error) {
	if options.Executable != "" {
		if _, err := claude.ResolveNativeExecutable(options.Executable); err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(options.Directory) || !filepath.IsAbs(options.Workspace) || !opaquePattern.MatchString(options.NativeScopeID) {
		return nil, errors.New("Invalid Claude executor private directory, workspace or scope")
	}
	if options.UploadDirectory != "" && !absoluteWorkspace(options.UploadDirectory) {
		return nil, errors.New("Invalid Claude upload directory")
	}
	if options.Launch == nil {
		options.Launch = launchClaudeExecutorRunner
	}
	// Old brokers keep their accepted work and original permissions. New sessions
	// use a separate private broker instead of rewriting a live owner's config.
	var legacy *ClaudeExecutorSessions
	if options.UploadDirectory != "" {
		previous, err := decodeClaudeConfig(options.Directory)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err == nil && previous.UploadDirectory == "" {
			if previous.Scope != options.NativeScopeID || previous.Workspace != options.Workspace || previous.Executable != options.Executable {
				return nil, errors.New("Claude broker ownership configuration changed")
			}
			oldOptions := options
			oldOptions.UploadDirectory = ""
			legacy, err = NewClaudeExecutorSessions(oldOptions)
			if err != nil {
				return nil, err
			}
			options.Directory = filepath.Join(options.Directory, "attachments-v1")
			options.ReadOnly = legacy
		}
	}
	return &ClaudeExecutorSessions{options: options, client: &http.Client{Timeout: 50 * time.Second}, legacy: legacy}, nil
}

func launchClaudeExecutorRunner(directory string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, ClaudeExecutorRunnerArgument, directory)
	command.Dir = directory
	command.SysProcAttr = detachedProcessAttributes()
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func (s *ClaudeExecutorSessions) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.client.CloseIdleConnections()
	if s.legacy != nil {
		s.legacy.Close()
	}
}

func (s *ClaudeExecutorSessions) endpoint(ctx context.Context, mayStart bool) (claudeBrokerConfig, claudeBrokerState, error) {
	s.opening.Lock()
	defer s.opening.Unlock()
	var config claudeBrokerConfig
	var state claudeBrokerState
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return config, state, errors.New("Claude execution client is closed")
	}
	config, err := decodeClaudeConfig(s.options.Directory)
	if errors.Is(err, fs.ErrNotExist) && mayStart {
		if err = os.MkdirAll(s.options.Directory, 0o700); err != nil {
			return config, state, err
		}
		token, err := executorSecret()
		if err != nil {
			return config, state, err
		}
		config = claudeBrokerConfig{Token: token, Workspace: s.options.Workspace, Scope: s.options.NativeScopeID,
			Executable: s.options.Executable, UploadDirectory: s.options.UploadDirectory}
		if err = writeExclusive(filepath.Join(s.options.Directory, claudeBrokerConfigName), Marshal(config)); err != nil {
			return config, state, err
		}
		if err = s.options.Launch(s.options.Directory); err != nil {
			return config, state, err
		}
	} else if err != nil {
		return config, state, err
	}
	if config.Scope != s.options.NativeScopeID || config.Workspace != s.options.Workspace ||
		config.Executable != s.options.Executable || config.UploadDirectory != s.options.UploadDirectory {
		return config, state, errors.New("Claude broker ownership configuration changed")
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := ReadExecutorFile(s.options.Directory, claudeBrokerStateName)
		if err == nil {
			if json.Unmarshal(Marshal(value), &state) != nil {
				return config, state, errors.New("Invalid Claude broker readiness")
			}
			endpoint, err := url.Parse(state.URL)
			if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.User != nil || state.PID <= 0 {
				return config, state, errors.New("Invalid Claude broker endpoint")
			}
			return config, state, nil
		}
		if !errors.Is(err, fs.ErrNotExist) || !mayStart {
			return config, state, err
		}
		select {
		case <-ctx.Done():
			return config, state, ctx.Err()
		case <-deadline.C:
			return config, state, errors.New("Claude broker readiness is unknown; no replacement was started")
		case <-ticker.C:
		}
	}
}

func (s *ClaudeExecutorSessions) call(ctx context.Context, request claudeBrokerRequest, mayStart bool) (claudeBrokerResponse, error) {
	var response claudeBrokerResponse
	if err := protocol.ValidateScope(ctx); err != nil {
		return response, err
	}
	config, state, err := s.endpoint(ctx, mayStart)
	if errors.Is(err, fs.ErrNotExist) && s.legacy != nil && request.ID != "" {
		return s.legacy.call(ctx, request, false)
	}
	if err != nil {
		return response, err
	}
	request.Binding = protocol.AccessBinding(ctx)
	body := bytes.NewReader(Marshal(request))
	httpRequest, err := http.NewRequestWithContext(ctx, "POST", state.URL+"/call", body)
	if err != nil {
		return response, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+config.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := s.client.Do(httpRequest)
	if err != nil {
		return response, errors.New("Claude broker delivery or observation is unknown; input was not replayed")
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		return response, errors.New("Claude broker request was rejected")
	}
	data, err := io.ReadAll(io.LimitReader(httpResponse.Body, 2<<20))
	if err != nil {
		return response, err
	}
	if json.Unmarshal(data, &response) != nil {
		return response, errors.New("Invalid Claude broker response")
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	if response.TimestampVersion < 0 || response.TimestampVersion > 1 {
		return response, errors.New("Unsupported Claude broker timestamp version")
	}
	if response.TimestampVersion == 0 {
		// An earlier live broker retains accepted work and uses fractional seconds.
		// Normalize only its private response; never restart or rewrite its owner.
		if response.Session != nil {
			response.Session.UpdatedAt = math.Trunc(response.Session.UpdatedAt)
		}
		for index := range response.Sessions {
			response.Sessions[index].UpdatedAt = math.Trunc(response.Sessions[index].UpdatedAt)
		}
		if response.Page != nil {
			for index := range response.Page.Messages {
				row := &response.Page.Messages[index]
				row.Timestamp = protocol.Number(math.Round(float64(row.Timestamp) * 1000))
			}
			for index := range response.Page.Nodes {
				row := &response.Page.Nodes[index]
				row.Timestamp = protocol.Number(math.Round(float64(row.Timestamp) * 1000))
			}
		}
	}
	if response.Missing && s.legacy != nil && request.ID != "" {
		return s.legacy.call(ctx, request, false)
	}
	return response, nil
}

func (s *ClaudeExecutorSessions) Capabilities() protocol.AdapterCapabilities {
	caps := claude.ExecutionCapabilities()
	if _, ok := s.options.ReadOnly.(*claude.Adapter); ok {
		// The source also routes live Mod sessions; session access keeps owned
		// stream-json sessions from acquiring unsupported steering.
		caps.ActiveSend = claude.Capabilities().ActiveSend
	}
	caps.Uploads = s.options.UploadDirectory != ""
	return caps
}
func (s *ClaudeExecutorSessions) Status() AdapterStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := s.call(ctx, claudeBrokerRequest{Operation: "status"}, false)
	if err == nil && response.Connected {
		return sourceStatus("connected", "")
	}
	if s.legacy != nil {
		return s.legacy.Status()
	}
	return sourceStatus("unavailable", "claude_native_handshake_unavailable")
}

func (s *ClaudeExecutorSessions) Create(context.Context, string, string, protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	return protocol.Session{}, protocol.SendReceipt{}, sourceUnsupported()
}

func (s *ClaudeExecutorSessions) Models(ctx context.Context) ([]protocol.Model, error) {
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("Claude execution client is closed")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Initialization supplies native metadata without a user message, session
	// creation or execution broker. Never borrow an accepted session's lifetime.
	native, err := claude.StartNative(ctx, claude.NativeOptions{Executable: s.options.Executable,
		Workspace: s.options.Workspace, Scope: s.options.NativeScopeID})
	if err != nil {
		return nil, err
	}
	defer native.CloseIdle()
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	closed = s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("Claude execution client is closed")
	}
	return native.Models(), ctx.Err()
}
