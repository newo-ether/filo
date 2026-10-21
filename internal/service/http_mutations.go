package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

func (s *Server) mutate(w *tracking, r *http.Request, id, action string) error {
	input, err := readBody(r)
	if err != nil {
		return s.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	}
	ctx := s.mutationContext(r, id, action)
	_, scoped := input["nativeScopeId"]
	if raw, present := input["accessBinding"]; present || scoped {
		delete(input, "accessBinding")
		binding, ok := raw.(string)
		if s.layer != "worker" || !ok || binding == "" || len(binding) > 24576 {
			return s.respond(w, http.StatusConflict, errorResponse{Error: "Invalid native access binding"})
		}
		adapter := s.adapters[codexDesktopSource]
		if adapter == nil {
			return s.respond(w, http.StatusConflict, errorResponse{Error: "Native access is unavailable"})
		}
		view, err := adapter.view(ctx)
		if err != nil {
			return err
		}
		ctx, err = view.expectedScope(ctx, input)
		if err != nil {
			return err
		}
		provider, ok := s.sessions.(SessionAccessProvider)
		if !ok {
			return s.respond(w, http.StatusConflict, errorResponse{Error: "Native access is unavailable"})
		}
		ctx = protocol.WithAccessBinding(ctx, binding)
		if err := provider.ValidateAccess(ctx, id, binding); err != nil {
			return err
		}
	}
	return s.mutateInput(w, ctx, id, action, input)
}

func (s *Server) mutateInput(w *tracking, ctx context.Context, id, action string, input map[string]any) error {
	switch action {
	case "rename":
		name, ok := input["name"].(string)
		if !ok || strings.TrimSpace(name) == "" || toolpreview.UTF16Len(name) > maxRenameUnits {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Expected a task name"})
		}
		session, err := s.sessions.Rename(ctx, id, name)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, session)
	case "archive":
		cwd, err := s.sessions.Archive(ctx, id)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, archiveResponse{Archived: true, Cwd: cwd})
	case "settings":
		settings, err := codex.ParseSettings(input)
		if err != nil {
			return err
		}
		if err := s.sessions.UpdateSettings(ctx, id, settings); err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, updatedResponse{Updated: true})
	case "model":
		model, ok := input["model"].(string)
		if !ok || strings.TrimSpace(model) == "" {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Expected model"})
		}
		if err := s.sessions.SetModel(ctx, id, model); err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, updatedResponse{Updated: true})
	case "stop":
		turnID, ok := input["turnId"].(string)
		if !ok || turnID == "" {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Expected turnId"})
		}
		if err := s.sessions.Stop(ctx, id, turnID); err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, stoppedResponse{Stopped: true})
	case "messages":
		text, clientID, err := s.messageText(input)
		if err != nil {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		}
		receipt, err := s.sessions.Send(ctx, id, text, clientID)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusAccepted, receipt)
	}
	return s.respond(w, http.StatusNotFound, errorResponse{Error: "Not found"})
}
