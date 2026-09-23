package codex

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/newo-ether/filo/internal/protocol"
)

// NativeMetadata exposes peripheral native APIs only. It has no create, resume,
// turn, stop, signal or retry operation and owns no original Desktop process.
type NativeMetadata struct {
	rpc      hostReader
	mu       sync.Mutex
	draining bool
	writes   int
}

func NewNativeMetadata(rpc hostReader) *NativeMetadata { return &NativeMetadata{rpc: rpc} }

func (m *NativeMetadata) beginMutation() (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.draining {
		return nil, metadataClosing()
	}
	m.writes++
	return func() { m.mu.Lock(); m.writes--; m.mu.Unlock() }, nil
}

func metadataClosing() *RpcError {
	return &RpcError{Message: "Filo auxiliary service is shutting down", Code: f64(-32600)}
}

type metadataThread struct {
	ID        string         `json:"id"`
	Name      *protocol.Text `json:"name"`
	Preview   *protocol.Text `json:"preview"`
	Cwd       *string        `json:"cwd"`
	UpdatedAt float64        `json:"updatedAt"`
	Status    *struct {
		Type *string `json:"type"`
	} `json:"status"`
}

func (thread metadataThread) session() protocol.Session {
	var title protocol.Text
	if thread.Name != nil {
		title = *thread.Name
	}
	if title == "" && thread.Preview != nil {
		title = *thread.Preview
	}
	if title == "" {
		title = protocol.Text(thread.ID)
	}
	var status *string
	if thread.Status != nil {
		status = thread.Status.Type
	}
	cwd := ""
	if thread.Cwd != nil {
		cwd = *thread.Cwd
	}
	return protocol.Session{ID: thread.ID, Title: title, Cwd: cwd, UpdatedAt: thread.UpdatedAt, Status: protocol.Known(status)}
}

func decodeMetadata(value any, result any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, result)
}

func (m *NativeMetadata) Rename(id, name string) (protocol.Session, error) {
	finish, err := m.beginMutation()
	if err != nil {
		return protocol.Session{}, err
	}
	defer finish()
	if trimJSWhitespace(name) == "" || utf16Len(name) > 4096 {
		return protocol.Session{}, &RpcError{Message: "Expected a task name", Code: f64(-32602)}
	}
	if _, err := m.rpc.Request("thread/name/set", map[string]any{"threadId": id, "name": protocol.Text(name)}); err != nil {
		return protocol.Session{}, err
	}
	value, err := m.rpc.Request("thread/read", map[string]any{"threadId": id})
	if err != nil {
		return protocol.Session{}, err
	}
	var response struct {
		Thread metadataThread `json:"thread"`
	}
	if err := decodeMetadata(value, &response); err != nil {
		return protocol.Session{}, err
	}
	return response.Thread.session(), nil
}

func (m *NativeMetadata) Archive(id string) (string, error) {
	finish, err := m.beginMutation()
	if err != nil {
		return "", err
	}
	defer finish()
	value, err := m.rpc.Request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
	if err != nil {
		return "", err
	}
	var response struct {
		Thread metadataThread `json:"thread"`
	}
	if err := decodeMetadata(value, &response); err != nil {
		return "", err
	}
	if response.Thread.ID != id || response.Thread.Cwd == nil {
		return "", errors.New("Native archive target is unavailable")
	}
	if _, err := m.rpc.Request("thread/archive", map[string]any{"threadId": id}); err != nil {
		return "", err
	}
	return *response.Thread.Cwd, nil
}

func (m *NativeMetadata) Models() ([]protocol.Model, error) {
	value, err := m.rpc.Request("model/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []struct {
			Model                     string        `json:"model"`
			DisplayName               protocol.Text `json:"displayName"`
			IsDefault                 bool          `json:"isDefault"`
			SupportedReasoningEfforts []struct {
				ReasoningEffort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
			DefaultReasoningEffort protocol.Optional[*string] `json:"defaultReasoningEffort"`
			ServiceTiers           []protocol.ServiceTier     `json:"serviceTiers"`
			DefaultServiceTier     protocol.Optional[*string] `json:"defaultServiceTier"`
		} `json:"data"`
	}
	if err := decodeMetadata(value, &response); err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, errors.New("Native model catalog is unavailable")
	}
	models := make([]protocol.Model, 0, len(response.Data))
	for _, native := range response.Data {
		var efforts []string
		if native.SupportedReasoningEfforts != nil {
			efforts = make([]string, 0, len(native.SupportedReasoningEfforts))
			for _, effort := range native.SupportedReasoningEfforts {
				efforts = append(efforts, effort.ReasoningEffort)
			}
		}
		models = append(models, protocol.Model{ID: native.Model, Name: native.DisplayName, IsDefault: native.IsDefault,
			ReasoningEfforts: efforts, DefaultReasoningEffort: native.DefaultReasoningEffort, ServiceTiers: native.ServiceTiers, DefaultServiceTier: native.DefaultServiceTier})
	}
	return models, nil
}

func (m *NativeMetadata) PrepareShutdown() error {
	m.mu.Lock()
	if m.draining {
		m.mu.Unlock()
		return metadataClosing()
	}
	m.draining = true
	if m.writes > 0 {
		m.draining = false
		m.mu.Unlock()
		return &RpcError{Message: "An auxiliary operation is still running", Code: f64(-32600)}
	}
	m.mu.Unlock()
	_, err := VerifyHostIdle(m.rpc)
	if err != nil {
		m.mu.Lock()
		m.draining = false
		m.mu.Unlock()
	}
	return err
}
