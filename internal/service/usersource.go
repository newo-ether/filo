package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

// userSource keeps CLI and Mod access in the selected ordinary user's helper.
// It forwards one explicit source path and never falls back to Desktop or another
// executor. Control requires fresh private scope and exact session binding.
type userSource struct {
	worker       *UserWorker
	id           string
	mu           sync.Mutex
	access       map[string]protocol.SessionAccess
	currentScope string
	streaming    map[uint64]func()
	nextStream   uint64
	changed      func(string)
	// attachments is set when the gateway owns an upload store for this source.
	// Uploads resolve to path references at the gateway. Claude must also prove
	// its native executor was started with access to the upload directory.
	attachments bool
}

func newUserSource(worker *UserWorker, id string, changed func(string)) *userSource {
	return &userSource{worker: worker, id: id, access: make(map[string]protocol.SessionAccess), streaming: make(map[uint64]func()), changed: changed}
}

func (source *userSource) descriptor(ctx context.Context) (protocol.AdapterDescriptor, error) {
	body, err := sourceInfoBody(ctx, source.worker)
	if err != nil {
		return protocol.AdapterDescriptor{}, err
	}
	var info struct {
		Extensions map[string]int               `json:"extensions"`
		Adapters   []protocol.AdapterDescriptor `json:"adapters"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return protocol.AdapterDescriptor{}, err
	}
	if info.Extensions["multiAdapter"] != protocol.MultiAdapterVersion || len(info.Adapters) > protocol.MaxAdapters {
		return protocol.AdapterDescriptor{}, errors.New("Incompatible private source service")
	}
	for _, descriptor := range info.Adapters {
		if descriptor.ID == source.id {
			if !opaquePattern.MatchString(descriptor.NativeScopeID) && descriptor.NativeScopeID != "" {
				return protocol.AdapterDescriptor{}, errors.New("Invalid private native scope")
			}
			source.mu.Lock()
			var cancelled []func()
			if source.currentScope != descriptor.NativeScopeID {
				clear(source.access)
				for id, cancel := range source.streaming {
					cancelled = append(cancelled, cancel)
					delete(source.streaming, id)
				}
				source.currentScope = descriptor.NativeScopeID
			}
			source.mu.Unlock()
			for _, cancel := range cancelled {
				cancel()
			}
			return descriptor, nil
		}
	}
	return protocol.AdapterDescriptor{}, errors.New("Configured private source is unavailable")
}

func (source *userSource) scope(ctx context.Context) (string, error) {
	descriptor, err := source.descriptor(ctx)
	if err != nil {
		return "", err
	}
	if descriptor.NativeScopeID == "" {
		return "", errors.New("Private native scope is unavailable")
	}
	return descriptor.NativeScopeID, nil
}

func (source *userSource) status(ctx context.Context) AdapterStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	descriptor, err := source.descriptor(ctx)
	if err != nil {
		return sourceStatus("unavailable", "private_source_unavailable")
	}
	return AdapterStatus{State: descriptor.State, Error: descriptor.Error}
}

func (source *userSource) path(tail string) string {
	return "/v1/adapters/" + source.id + "/" + tail
}

func (source *userSource) remember(id string, access *protocol.SessionAccess) {
	if access == nil {
		return
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.currentScope != "" && access.NativeScopeID != source.currentScope {
		return
	}
	if len(source.access) >= 64 {
		clear(source.access)
	}
	source.access[id] = *access
}

func (source *userSource) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	path := source.path("sessions")
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	body, err := source.worker.call(ctx, path, nil)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	var page protocol.SessionPage
	if err := json.Unmarshal(body, &page); err != nil {
		return page, err
	}
	for _, session := range page.Sessions {
		source.remember(session.ID, session.Access)
	}
	return page, nil
}

func (source *userSource) Read(ctx context.Context, id, cursor string, activity bool,
	excludedTurns []string) (protocol.ConversationPage, error) {
	if !validSessionRef(id) || len(excludedTurns) != 0 {
		return protocol.ConversationPage{}, errors.New("Invalid private source read")
	}
	query := url.Values{}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if activity {
		query.Set("includeActivity", "true")
	}
	path := source.path("sessions/" + url.PathEscape(id))
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body, err := source.worker.call(ctx, path, nil)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	var page protocol.ConversationPage
	if err := json.Unmarshal(body, &page); err != nil {
		return page, err
	}
	source.remember(id, page.Access)
	return page, nil
}

func (source *userSource) CatalogAccess(ctx context.Context, id string) (protocol.SessionAccess, error) {
	source.mu.Lock()
	access, ok := source.access[id]
	currentScope := source.currentScope
	source.mu.Unlock()
	if ok && access.NativeScopeID != currentScope {
		ok = false
	}
	if !ok {
		page, err := source.Read(ctx, id, "", false, nil)
		if err != nil {
			return protocol.SessionAccess{}, err
		}
		if page.Access == nil {
			return protocol.SessionAccess{}, errors.New("Private source identity is unavailable")
		}
		access = *page.Access
	}
	source.mu.Lock()
	currentScope = source.currentScope
	source.mu.Unlock()
	if access.NativeScopeID == "" || access.NativeScopeID != currentScope {
		return protocol.SessionAccess{}, sourceUnsupported()
	}
	access.Capabilities = readOnlyCaps(access.Capabilities)
	return access, nil
}

func (source *userSource) Attach(ctx context.Context, id string) (func(), error) {
	if !validSessionRef(id) {
		return nil, errors.New("Invalid private source session")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	initialScope, err := source.scope(streamCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	response, err := source.worker.openStream(streamCtx, source.path("sessions/"+url.PathEscape(id)+"/events"))
	if err != nil {
		cancel()
		return nil, err
	}
	if response == nil || response.Body == nil || response.StatusCode != 200 {
		cancelTaskEventResponse(response)
		cancel()
		return nil, errors.New("Private source observation is unavailable")
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			_ = response.Body.Close()
		})
	}
	source.mu.Lock()
	if source.currentScope != initialScope || len(source.streaming) >= 64 {
		source.mu.Unlock()
		release()
		return nil, sourceUnsupported()
	}
	source.nextStream++
	streamID := source.nextStream
	source.streaming[streamID] = release
	source.mu.Unlock()
	go func() {
		defer release()
		defer func() {
			source.mu.Lock()
			delete(source.streaming, streamID)
			source.mu.Unlock()
		}()
		err := ReadTaskEvents(response, func(value any) error {
			page, err := taskPage(value)
			if err != nil {
				return err
			}
			source.mu.Lock()
			validScope := source.currentScope == initialScope
			source.mu.Unlock()
			if !validScope {
				return sourceUnsupported()
			}
			source.remember(id, page.Access)
			if source.changed != nil && streamCtx.Err() == nil {
				source.changed(id)
			}
			return nil
		})
		if err != nil && streamCtx.Err() == nil && source.changed != nil {
			source.mu.Lock()
			delete(source.access, id)
			source.mu.Unlock()
			source.changed(id)
		}
	}()
	return release, nil
}

func (source *userSource) Models(ctx context.Context) ([]protocol.Model, error) {
	body, err := source.worker.call(ctx, source.path("models"), nil)
	if err != nil {
		return nil, err
	}
	var value modelsResponse
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	return value.Models, nil
}

func (source *userSource) Usage(ctx context.Context) (protocol.AccountUsage, error) {
	body, err := source.worker.call(ctx, source.path("usage"), nil)
	if err != nil {
		return protocol.AccountUsage{}, err
	}
	var value protocol.AccountUsage
	err = json.Unmarshal(body, &value)
	return value, err
}

func sourceUnsupported() error {
	return errors.New("Operation is unsupported by this source/session")
}

var _ SessionAPI = (*userSource)(nil)
