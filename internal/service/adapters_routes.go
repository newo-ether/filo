package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

func (v *adapterView) unsupported(w *tracking) error {
	return v.server.respond(w, http.StatusNotImplemented, errorResponse{Error: "Operation is unsupported by this source/session"})
}

func (v *adapterView) route(w *tracking, r *http.Request, path string) error {
	ctx := withSourceReadScope(r.Context(), v.scope)
	r = r.Clone(ctx)
	switch path {
	case "/v1/sessions":
		switch r.Method {
		case http.MethodGet:
			if !v.Capabilities.History {
				return v.unsupported(w)
			}
			page, err := v.catalog(ctx, r.URL.Query().Get("cursor"))
			if err != nil {
				return err
			}
			return v.server.respond(w, http.StatusOK, page)
		case http.MethodPost:
			return v.create(w, r)
		}
	case "/v1/models":
		if r.Method == http.MethodGet {
			if !v.Capabilities.Models {
				return v.unsupported(w)
			}
			models, err := v.Sessions.Models(ctx)
			if err != nil {
				return err
			}
			if err := v.check(ctx); err != nil {
				return err
			}
			return v.server.respond(w, http.StatusOK, modelsResponse{Models: models})
		}
	case "/v1/usage":
		if r.Method == http.MethodGet {
			if !v.Capabilities.Usage || v.Usage == nil {
				return v.unsupported(w)
			}
			usage, err := v.Usage(ctx)
			if err != nil {
				return err
			}
			if err := v.check(ctx); err != nil {
				return err
			}
			return v.server.respond(w, http.StatusOK, usage)
		}
	case "/v1/sessions/status":
		return v.statuses(w, r)
	}
	if path == "/v1/uploads" || strings.HasPrefix(path, "/v1/uploads/") {
		return v.upload(w, r, path)
	}
	id, action, ok := sessionRoute(path)
	if !ok || !validSessionRef(id) || action == "access" {
		return v.server.respond(w, http.StatusNotFound, errorResponse{Error: "Not found"})
	}
	if r.Method == http.MethodPost {
		return v.mutate(w, r, id, action)
	}
	if r.Method != http.MethodGet {
		return v.server.respond(w, http.StatusNotFound, errorResponse{Error: "Not found"})
	}
	if !v.Capabilities.History {
		return v.unsupported(w)
	}
	if action == "events" && !v.Capabilities.Live {
		return v.unsupported(w)
	}
	if (action == "payloads" || action == "topology" || action == "image") && !v.Capabilities.LazyMessages {
		return v.unsupported(w)
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" {
		var err error
		cursor, err = v.unseal("history", id, cursor)
		if err != nil {
			return err
		}
	}
	key := v.key(id)
	switch action {
	case "":
		var page protocol.ConversationPage
		var err error
		if r.URL.Query().Get("includeMetadata") == "true" {
			page, err = v.server.payloads.Page(ctx, v.server.sessions, key, cursor, v.server.layer, nil)
		} else {
			page, err = ReadConversationPage(ctx, v.server.sessions, key, cursor, PageOptions{Activity: r.URL.Query().Get("includeActivity") == "true", Layer: v.server.layer})
		}
		if err != nil {
			return err
		}
		if err := v.check(ctx); err != nil {
			return err
		}
		return v.server.respond(w, http.StatusOK, v.projectPage(id, page))
	case "topology":
		page, err := v.server.payloads.Topology(ctx, v.server.sessions, key, cursor, v.server.layer)
		if err != nil {
			return err
		}
		if err := v.check(ctx); err != nil {
			return err
		}
		return v.server.respond(w, http.StatusOK, v.projectTopology(id, page))
	case "payloads":
		raw, err := queryValue(r.URL.Query(), "messages")
		if err != nil {
			return CodexRpcError("Invalid message identities", -32602)
		}
		requests, _ := payloadRequests(raw)
		value, err := v.server.payloads.Load(ctx, v.server.sessions, key, requests, v.server.layer, false)
		if err != nil {
			return err
		}
		if err := v.check(ctx); err != nil {
			return err
		}
		return v.server.respond(w, http.StatusOK, value)
	case "image":
		return v.server.image(w, r, key)
	case "events":
		view := r.URL.Query().Get("view")
		var source *ConversationPayloads
		if view == "topology" || view == "paged" {
			source = v.server.payloads
		}
		return v.server.stream(ctx, w, key, source, view == "paged")
	}
	return v.server.respond(w, http.StatusNotFound, errorResponse{Error: "Not found"})
}

func (v *adapterView) statuses(w *tracking, r *http.Request) error {
	if r.Method != http.MethodGet {
		return v.unsupported(w)
	}
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(ids) == 0 || len(ids) > 12 {
		return CodexRpcError("Expected 1-12 session identities", -32602)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validSessionRef(id) || seen[id] {
			return CodexRpcError("Invalid session identities", -32602)
		}
		seen[id] = true
	}
	source, ok := v.Sessions.(StatusSource)
	if !ok {
		return v.unsupported(w)
	}
	statuses, err := source.Statuses(r.Context(), ids)
	if err != nil {
		return err
	}
	if err := v.check(r.Context()); err != nil {
		return err
	}
	return v.server.respond(w, http.StatusOK, statusesResponse{Statuses: statuses})
}

func (v *adapterView) expectedScope(ctx context.Context, input map[string]any) (context.Context, error) {
	expected, ok := input["nativeScopeId"].(string)
	delete(input, "nativeScopeId")
	if !ok || expected != v.scope {
		return ctx, CodexRpcError("Native source identity changed; refresh sources", -32600)
	}
	if err := v.check(ctx); err != nil {
		return ctx, err
	}
	return protocol.WithNativeScopeID(protocol.WithScopeValidation(ctx, v.check), v.scope), nil
}

func (v *adapterView) create(w *tracking, r *http.Request) error {
	if !v.Capabilities.Create {
		return v.unsupported(w)
	}
	input, err := readBody(r)
	if err != nil {
		return v.server.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	}
	ctx, err := v.expectedScope(r.Context(), input)
	if err != nil {
		return err
	}
	if _, present := input["accessBinding"]; present {
		return CodexRpcError("A new session cannot carry an existing binding", -32602)
	}
	if err := v.attachments(input); err != nil {
		return err
	}
	text, clientID, err := v.server.messageText(input)
	if err != nil {
		return err
	}
	settings := protocol.SessionSettings{}
	if raw, present := input["settings"]; present {
		object, ok := raw.(map[string]any)
		if !ok {
			return errors.New("Expected settings object")
		}
		if err := settingsAllowed(object, v.Capabilities); err != nil {
			return err
		}
		settings, err = codex.ParseSettings(object)
		if err != nil {
			return err
		}
	}
	if err := v.check(ctx); err != nil {
		return err
	}
	session, receipt, err := v.Sessions.Create(ctx, text, clientID, settings)
	if err != nil {
		return err
	}
	if err := v.check(ctx); err != nil {
		return err
	}
	session.Access, err = v.access(ctx, session.ID)
	if err != nil {
		return err
	}
	return v.server.respond(w, http.StatusCreated, createSessionResponse{Session: session, TurnID: receipt.TurnID, ClientID: receipt.ClientID})
}

func (v *adapterView) mutate(w *tracking, r *http.Request, id, action string) error {
	input, err := readBody(r)
	if err != nil {
		return v.server.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	}
	token, ok := input["accessBinding"].(string)
	delete(input, "accessBinding")
	if !ok {
		return CodexRpcError("Native access binding is required", -32600)
	}
	binding, err := v.unseal("binding", id, token)
	if err != nil {
		return err
	}
	access, err := v.access(r.Context(), id)
	if err != nil {
		return err
	}
	if access == nil || access.Binding != token {
		return CodexRpcError("Native access binding changed", -32600)
	}
	caps := access.Capabilities
	allowed := false
	switch action {
	case "messages":
		allowed = caps.IdleSend || caps.ActiveSend == "native-steer"
	case "stop":
		allowed = caps.Stop
	case "rename":
		allowed = caps.Rename
	case "archive":
		allowed = caps.Archive
	case "model":
		allowed = settingsAllowed(map[string]any{"model": true}, caps) == nil
	case "settings":
		allowed = len(caps.Settings) > 0
		if allowed {
			if err := settingsAllowed(input, caps); err != nil {
				return err
			}
		}
	}
	if !allowed {
		return v.unsupported(w)
	}
	if err := v.attachments(input); err != nil {
		return err
	}
	ctx := protocol.WithNativeScopeID(protocol.WithAccessBinding(protocol.WithScopeValidation(r.Context(), v.check), binding), v.scope)
	if err := v.check(ctx); err != nil {
		return err
	}
	if err := v.Sessions.(SessionAccessProvider).ValidateAccess(ctx, id, binding); err != nil {
		return err
	}
	// mutateInput uses the original native surface. No source is selected again.
	native := &Server{sessions: v.Sessions, uploads: v.Uploads, expected: v.root.expected}
	return native.mutateInput(w, ctx, id, action, input)
}

func settingsAllowed(input map[string]any, caps protocol.AdapterCapabilities) error {
	for field := range input {
		found := false
		for _, allowed := range caps.Settings {
			if field == allowed {
				found = true
				break
			}
		}
		if !found {
			return CodexRpcError("Setting is unsupported by this source/session", -32602)
		}
	}
	return nil
}

func (v *adapterView) attachments(input map[string]any) error {
	raw, present := input["attachments"]
	if !present {
		return nil
	}
	if !v.Capabilities.Uploads {
		return CodexRpcError("Attachments are unsupported by this source", -32602)
	}
	items, ok := raw.([]any)
	if !ok || len(items) > 16 {
		return errors.New("Invalid attachment identities")
	}
	ids := make([]any, 0, len(items))
	for _, item := range items {
		token, ok := item.(string)
		if !ok {
			return errors.New("Invalid attachment identity")
		}
		id, err := v.unseal("upload", "", token)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	input["attachments"] = ids
	return nil
}
