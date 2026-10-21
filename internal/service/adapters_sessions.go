package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/newo-ether/filo/internal/protocol"
)

// scopedSessions gives shared cache and event code a source-qualified key while
// preserving the original native ID at the adapter boundary.
type scopedSessions struct {
	SessionAPI
	view *adapterView
}

func (s *scopedSessions) Read(ctx context.Context, key, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	id, err := s.view.nativeID(key)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if err := s.view.check(ctx); err != nil {
		return protocol.ConversationPage{}, err
	}
	page, err := s.SessionAPI.Read(withSourceReadScope(ctx, s.view.scope), id, cursor, activity, excluded)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if err := s.view.check(ctx); err != nil {
		return protocol.ConversationPage{}, err
	}
	if page.Access != nil {
		page.Access, err = s.view.projectAccess(id, *page.Access)
	} else {
		page.Access, err = s.view.access(ctx, id)
	}
	return page, err
}

func (s *scopedSessions) Attach(ctx context.Context, key string) (func(), error) {
	id, err := s.view.nativeID(key)
	if err != nil {
		return nil, err
	}
	if err := s.view.check(ctx); err != nil {
		return nil, err
	}
	release, err := s.SessionAPI.Attach(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.view.check(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (v *adapterView) catalog(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	if cursor != "" {
		var err error
		cursor, err = v.unseal("catalog", "", cursor)
		if err != nil {
			return protocol.SessionPage{}, err
		}
	}
	if err := v.check(ctx); err != nil {
		return protocol.SessionPage{}, err
	}
	page, err := v.Sessions.List(withSourceReadScope(ctx, v.scope), cursor)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	// Catalog rows never acquire a native owner or follower. Existing verified
	// row identity is reused; an absent identity leaves this row read-only.
	for i := range page.Sessions {
		row := &page.Sessions[i]
		if !validSessionRef(row.ID) {
			return protocol.SessionPage{}, errors.New("Invalid native session reference")
		}
		if row.Access != nil {
			row.Access, err = v.projectAccess(row.ID, *row.Access)
			if err != nil {
				return protocol.SessionPage{}, err
			}
		} else if v.Family == "codex" {
			data, _ := json.Marshal([3]string{"codex", v.scope, row.ID})
			canonical := sha256.Sum256(data)
			row.Access = &protocol.SessionAccess{NativeScopeID: v.scope, CanonicalSessionRef: base64.RawURLEncoding.EncodeToString(canonical[:]),
				Binding: v.seal("catalog-binding", row.ID, "read-only"), Capabilities: readOnlyCaps(v.Capabilities)}
		}
	}
	if err := v.check(ctx); err != nil {
		return protocol.SessionPage{}, err
	}
	if page.NextCursor != nil && *page.NextCursor != "" {
		token := v.seal("catalog", "", *page.NextCursor)
		page.NextCursor = &token
	}
	return page, nil
}

func (v *adapterView) projectPage(id string, page protocol.ConversationPage) protocol.ConversationPage {
	if page.NextCursor != nil && *page.NextCursor != "" {
		token := v.seal("history", id, *page.NextCursor)
		page.NextCursor = &token
	}
	if page.PageCursor.Known && page.PageCursor.Value != "" {
		page.PageCursor = protocol.Known(v.seal("history", id, page.PageCursor.Value))
	}
	return page
}

func (v *adapterView) projectTopology(id string, page protocol.TopologyPage) protocol.TopologyPage {
	if page.NextCursor != nil && *page.NextCursor != "" {
		token := v.seal("history", id, *page.NextCursor)
		page.NextCursor = &token
	}
	return page
}

func (s *Server) projectStreamPage(key string, page any) (any, error) {
	if s.scoped == nil {
		return page, nil
	}
	id, err := s.scoped.nativeID(key)
	if err != nil {
		return nil, err
	}
	switch value := page.(type) {
	case protocol.ConversationPage:
		return s.scoped.projectPage(id, value), nil
	case protocol.TopologyPage:
		return s.scoped.projectTopology(id, value), nil
	}
	return nil, errors.New("Invalid scoped stream page")
}
