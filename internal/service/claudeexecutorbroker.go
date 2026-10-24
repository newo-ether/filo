package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

const ClaudeExecutorRunnerArgument = "claude-executor-runner"
const claudeBrokerConfigName = "claude-broker-config.json"
const claudeBrokerStateName = "claude-broker-state.json"
const claudeBrokerClaimName = "claude-broker-started"

type claudeBrokerConfig struct {
	Token           string `json:"token"`
	Workspace       string `json:"workspace"`
	UploadDirectory string `json:"uploadDirectory,omitempty"`
	Scope           string `json:"scope"`
	Executable      string `json:"executable,omitempty"`
}

type claudeBrokerState struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

type claudeBrokerRequest struct {
	Operation string                   `json:"operation"`
	ID        string                   `json:"id,omitempty"`
	Text      string                   `json:"text,omitempty"`
	ClientID  string                   `json:"clientId,omitempty"`
	TurnID    string                   `json:"turnId,omitempty"`
	Model     string                   `json:"model,omitempty"`
	Binding   string                   `json:"binding,omitempty"`
	Settings  protocol.SessionSettings `json:"settings"`
}

type claudeBrokerResponse struct {
	TimestampVersion int                        `json:"timestampVersion,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Missing          bool                       `json:"missing,omitempty"`
	Session          *protocol.Session          `json:"session,omitempty"`
	Receipt          *protocol.SendReceipt      `json:"receipt,omitempty"`
	Page             *protocol.ConversationPage `json:"page,omitempty"`
	Sessions         []protocol.Session         `json:"sessions,omitempty"`
	Models           []protocol.Model           `json:"models,omitempty"`
	Connected        bool                       `json:"connected"`
}

type claudeCreation struct {
	text     string
	settings protocol.SessionSettings
	native   *claude.NativeSession
	done     chan struct{}
	err      error
}

type claudeBroker struct {
	mu       sync.Mutex
	config   claudeBrokerConfig
	sessions map[string]*claude.NativeSession
	creating map[string]*claudeCreation
}

func decodeClaudeConfig(directory string) (claudeBrokerConfig, error) {
	var config claudeBrokerConfig
	value, err := ReadExecutorFile(directory, claudeBrokerConfigName)
	if err != nil {
		return config, err
	}
	if err = json.Unmarshal(Marshal(value), &config); err != nil {
		return config, err
	}
	if !tokenPattern.MatchString(config.Token) || !opaquePattern.MatchString(config.Scope) || !absoluteWorkspace(config.Workspace) ||
		(config.UploadDirectory != "" && !absoluteWorkspace(config.UploadDirectory)) {
		return config, errors.New("Invalid private Claude broker configuration")
	}
	return config, nil
}

// RunClaudeExecutorRunner owns the stream-json processes and their pipes. Its
// lifetime is independent of the gateway and every HTTP request or follower.
func RunClaudeExecutorRunner(arguments []string) error {
	if len(arguments) != 1 || !filepath.IsAbs(arguments[0]) {
		return errors.New("Expected one absolute private Claude broker directory")
	}
	directory := arguments[0]
	config, err := decodeClaudeConfig(directory)
	if err != nil {
		return err
	}
	if err := writeExclusive(filepath.Join(directory, claudeBrokerClaimName), Marshal(map[string]int{"pid": os.Getpid()})); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	broker := &claudeBroker{config: config, sessions: map[string]*claude.NativeSession{}, creating: map[string]*claudeCreation{}}
	server := &http.Server{Handler: broker, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: "http://" + listener.Addr().String(), PID: os.Getpid()}); err != nil {
		return err
	}
	return server.Serve(listener)
}

func (b *claudeBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/call" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.config.Token)) != 1 {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	var request claudeBrokerRequest
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&request) != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	// Native work is owned by this process. HTTP cancellation only abandons
	// observation; it cannot revoke an admitted generation or close stdin.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ctx = protocol.WithAccessBinding(ctx, request.Binding)
	response, err := b.call(ctx, request)
	if err != nil {
		response.Error = err.Error()
	}
	response.TimestampVersion = 1
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (b *claudeBroker) call(ctx context.Context, r claudeBrokerRequest) (claudeBrokerResponse, error) {
	var response claudeBrokerResponse
	if r.Operation == "status" {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, session := range b.sessions {
			if session.Alive() {
				response.Connected = true
				break
			}
		}
		return response, nil
	}
	if r.Operation == "list" {
		b.mu.Lock()
		defer b.mu.Unlock()
		response.Sessions = []protocol.Session{}
		for _, session := range b.sessions {
			response.Sessions = append(response.Sessions, session.Session())
		}
		return response, nil
	}
	if r.Operation == "create" {
		return b.create(ctx, r)
	}
	if r.Operation == "models" {
		b.mu.Lock()
		for _, session := range b.sessions {
			response.Models = session.Models()
			b.mu.Unlock()
			return response, nil
		}
		b.mu.Unlock()
		return response, errors.New("Claude models require an existing handshaken Filo execution")
	}
	b.mu.Lock()
	session := b.sessions[r.ID]
	b.mu.Unlock()
	if session == nil {
		response.Missing = true
		return response, nil
	}
	switch r.Operation {
	case "read":
		page := session.Read()
		response.Page = &page
	case "send":
		receipt, err := session.Send(ctx, r.Text, r.ClientID)
		response.Receipt = &receipt
		return response, err
	case "stop":
		return response, session.Stop(ctx, r.TurnID)
	case "model":
		return response, session.SetModel(ctx, r.Model)
	case "effort":
		return response, session.SetEffort(ctx, r.Settings.Effort.Value)
	case "rename":
		if err := session.Rename(ctx, r.Text); err != nil {
			return response, err
		}
		renamed := session.Session()
		response.Session = &renamed
	default:
		return response, errors.New("Unsupported Claude broker operation")
	}
	return response, nil
}

func (b *claudeBroker) create(ctx context.Context, r claudeBrokerRequest) (claudeBrokerResponse, error) {
	var response claudeBrokerResponse
	if r.ClientID == "" || len(r.ClientID) > 256 || len(r.Text) == 0 || len(r.Text) > 24<<10 || r.Settings.ServiceTier.Known {
		return response, errors.New("Unsupported Claude input or settings")
	}
	b.mu.Lock()
	creation := b.creating[r.ClientID]
	if creation != nil {
		if creation.text != r.Text || creation.settings != r.Settings {
			b.mu.Unlock()
			return response, errors.New("Claude create identity already has different input or settings")
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		case <-creation.done:
		}
	} else {
		if len(b.creating) >= 16 {
			b.mu.Unlock()
			return response, errors.New("Claude creation capacity reached")
		}
		active := 0
		for _, entry := range b.creating {
			if entry.native == nil && entry.err == nil || entry.native != nil && entry.native.Alive() {
				active++
			}
		}
		if active >= 4 {
			b.mu.Unlock()
			return response, errors.New("Claude native executor capacity reached")
		}
		creation = &claudeCreation{text: r.Text, settings: r.Settings, done: make(chan struct{})}
		b.creating[r.ClientID] = creation
		b.mu.Unlock()
		native, err := claude.StartNative(ctx, claude.NativeOptions{Executable: b.config.Executable,
			Workspace: b.config.Workspace, UploadDirectory: b.config.UploadDirectory, Scope: b.config.Scope, OnChange: func(id string) {
				b.mu.Lock()
				if entry := b.creating[r.ClientID]; entry != nil && entry.native != nil {
					b.sessions[id] = entry.native
				}
				b.mu.Unlock()
			}})
		b.mu.Lock()
		creation.native = native
		b.mu.Unlock()
		if err == nil && r.Settings.Model.Known {
			err = native.SetModel(ctx, r.Settings.Model.Value)
		}
		if err == nil && r.Settings.Effort.Known {
			err = native.SetEffort(ctx, r.Settings.Effort.Value)
		}
		if err == nil {
			_, err = native.Send(ctx, r.Text, r.ClientID)
		}
		b.mu.Lock()
		creation.native, creation.err = native, err
		if native != nil && native.ID() != "" {
			b.sessions[native.ID()] = native
		}
		close(creation.done)
		b.mu.Unlock()
		if err != nil && native != nil {
			native.CloseIdle()
		}
	}
	if creation.err != nil {
		return response, creation.err
	}
	receipt, err := creation.native.Send(ctx, r.Text, r.ClientID)
	if err != nil {
		return response, err
	}
	session := creation.native.Session()
	response.Session = &session
	response.Receipt = &receipt
	return response, nil
}
