package service

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"sort"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

func (s *ClaudeExecutorSessions) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	if cursor != "" {
		return protocol.SessionPage{}, errors.New("Claude bounded catalog has no older page")
	}
	page := protocol.SessionPage{Sessions: []protocol.Session{}}
	if s.options.ReadOnly != nil {
		var err error
		page, err = s.options.ReadOnly.List(ctx, "")
		if err != nil {
			return page, err
		}
	}
	response, err := s.call(ctx, claudeBrokerRequest{Operation: "list"}, false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return page, err
	}
	page.Sessions = append(page.Sessions, response.Sessions...)
	sort.Slice(page.Sessions, func(i, j int) bool { return page.Sessions[i].ID < page.Sessions[j].ID })
	return page, nil
}

func (s *ClaudeExecutorSessions) Read(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	response, err := s.call(ctx, claudeBrokerRequest{Operation: "read", ID: id}, false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return protocol.ConversationPage{}, err
	}
	if response.Page != nil {
		if cursor != "" {
			return protocol.ConversationPage{}, errors.New("Claude execution history is bounded")
		}
		if !activity || len(excluded) > 0 {
			omitted := map[string]bool{}
			for _, turn := range excluded {
				omitted[turn] = true
			}
			rows := response.Page.Messages[:0]
			for _, row := range response.Page.Messages {
				if !omitted[row.TurnID] && (activity || row.Activity == nil) {
					rows = append(rows, row)
				}
			}
			response.Page.Messages = rows
		}
		return *response.Page, nil
	}
	if s.options.ReadOnly != nil {
		return s.options.ReadOnly.Read(ctx, id, cursor, activity, excluded)
	}
	return protocol.ConversationPage{}, errors.New("Claude session is outside Filo execution scope")
}

func (s *ClaudeExecutorSessions) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	page, err := s.Read(ctx, id, "", false, nil)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	if page.Access == nil {
		return protocol.SessionAccess{}, errors.New("Claude session access is unavailable")
	}
	return *page.Access, nil
}

func (s *ClaudeExecutorSessions) ValidateAccess(ctx context.Context, id, binding string) error {
	access, err := s.Access(ctx, id)
	if err != nil {
		return err
	}
	if binding == "" || access.Binding != binding {
		return errors.New("Claude session access binding changed")
	}
	return nil
}

func (s *ClaudeExecutorSessions) Send(ctx context.Context, id, text, client string) (protocol.SendReceipt, error) {
	if native, ok := s.options.ReadOnly.(*claude.Adapter); ok {
		if _, err := native.Access(ctx, id); err == nil {
			return native.Send(ctx, id, text, client)
		}
	}
	response, err := s.call(ctx, claudeBrokerRequest{Operation: "send", ID: id, Text: text, ClientID: client}, false)
	if err != nil {
		return protocol.SendReceipt{}, err
	}
	if response.Missing || response.Receipt == nil {
		return protocol.SendReceipt{}, sourceUnsupported()
	}
	return *response.Receipt, nil
}

func (s *ClaudeExecutorSessions) SetModel(ctx context.Context, id, model string) error {
	if native, ok := s.options.ReadOnly.(*claude.Adapter); ok {
		if _, err := native.Access(ctx, id); err == nil {
			return native.SetModel(ctx, id, model)
		}
	}
	response, err := s.call(ctx, claudeBrokerRequest{Operation: "model", ID: id, Model: model}, false)
	if err == nil && response.Missing {
		return sourceUnsupported()
	}
	return err
}

func (s *ClaudeExecutorSessions) Stop(ctx context.Context, id, turn string) error {
	if native, ok := s.options.ReadOnly.(*claude.Adapter); ok {
		if access, err := native.Access(ctx, id); err == nil {
			if access.Binding != protocol.AccessBinding(ctx) || !access.Capabilities.Stop {
				return sourceUnsupported()
			}
			return s.options.ReadOnly.Stop(ctx, id, turn)
		}
	}
	response, err := s.call(ctx, claudeBrokerRequest{Operation: "stop", ID: id, TurnID: turn}, false)
	if err == nil && response.Missing {
		return sourceUnsupported()
	}
	return err
}

func (s *ClaudeExecutorSessions) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	if native, ok := s.options.ReadOnly.(*claude.Adapter); ok {
		if _, err := native.Access(ctx, id); err == nil {
			return native.UpdateSettings(ctx, id, settings)
		}
	}
	if settings.ServiceTier.Known {
		return sourceUnsupported()
	}
	if settings.Model.Known {
		if err := s.SetModel(ctx, id, settings.Model.Value); err != nil {
			return err
		}
	}
	if settings.Effort.Known {
		response, err := s.call(ctx, claudeBrokerRequest{Operation: "effort", ID: id,
			Settings: protocol.SessionSettings{Effort: settings.Effort}}, false)
		if err == nil && response.Missing {
			return sourceUnsupported()
		}
		return err
	}
	return nil
}

func (s *ClaudeExecutorSessions) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	response, err := s.call(ctx, claudeBrokerRequest{Operation: "rename", ID: id, Text: name}, false)
	if err != nil {
		return protocol.Session{}, err
	}
	if response.Missing || response.Session == nil {
		return protocol.Session{}, sourceUnsupported()
	}
	if s.options.OnChange != nil {
		s.options.OnChange(id)
	}
	return *response.Session, nil
}
func (s *ClaudeExecutorSessions) Archive(context.Context, string) (string, error) {
	return "", sourceUnsupported()
}

func (s *ClaudeExecutorSessions) Attach(ctx context.Context, id string) (func(), error) {
	page, err := s.Read(ctx, id, "", true, nil)
	if err != nil {
		return nil, err
	}
	observation, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-observation.Done():
				return
			case <-ticker.C:
			}
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			next, err := s.Read(observation, id, "", true, nil)
			if err != nil {
				if s.options.OnChange != nil && observation.Err() == nil {
					s.options.OnChange(id)
				}
				return
			}
			if !reflect.DeepEqual(page, next) {
				page = next
				if s.options.OnChange != nil {
					s.options.OnChange(id)
				}
			}
		}
	}()
	return cancel, nil
}

var _ SessionAPI = (*ClaudeExecutorSessions)(nil)
var _ SessionAccessProvider = (*ClaudeExecutorSessions)(nil)
