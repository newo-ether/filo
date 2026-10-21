package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/newo-ether/filo/internal/uploads"
)

func (v *adapterView) upload(w *tracking, r *http.Request, path string) error {
	if !v.Capabilities.Uploads || v.Uploads == nil {
		return v.unsupported(w)
	}
	respond := func(status int, value any) error { return v.server.respond(w, status, value) }
	fail := func(err error) error {
		status := http.StatusBadRequest
		if errors.Is(err, uploads.ErrBusy) {
			status = http.StatusTooManyRequests
		}
		return respond(status, errorResponse{Error: err.Error()})
	}
	suffix := strings.TrimPrefix(path, "/v1/uploads")
	if r.Method == http.MethodPost && suffix == "" {
		input, err := readBody(r)
		if err != nil {
			return fail(err)
		}
		ctx, err := v.expectedScope(r.Context(), input)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) > 4096 {
			return fail(uploads.ErrInvalid)
		}
		var meta uploads.Metadata
		if json.Unmarshal(encoded, &meta) != nil {
			return fail(uploads.ErrInvalid)
		}
		if err := v.check(ctx); err != nil {
			return err
		}
		result, err := v.Uploads.Begin(meta)
		if err != nil {
			return fail(err)
		}
		if err := v.check(ctx); err != nil {
			_ = v.Uploads.Cancel(result.ID)
			return err
		}
		result.ID = v.seal("upload", "", result.ID)
		return respond(http.StatusCreated, result)
	}
	token, action, _ := strings.Cut(strings.TrimPrefix(suffix, "/"), "/")
	if token == "" || strings.Contains(action, "/") {
		return respond(http.StatusNotFound, errorResponse{Error: "Not found"})
	}
	id, err := v.unseal("upload", "", token)
	if err != nil {
		return err
	}
	if err := v.check(r.Context()); err != nil {
		return err
	}
	switch {
	case r.Method == http.MethodPut && action == "":
		offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil {
			return fail(uploads.ErrInvalid)
		}
		if r.ContentLength > uploads.MaxChunkBytes {
			return respond(http.StatusRequestEntityTooLarge, errorResponse{Error: "Attachment chunk is too large"})
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, uploads.MaxChunkBytes+1))
		if err != nil || len(data) > uploads.MaxChunkBytes {
			return fail(uploads.ErrInvalid)
		}
		if err := v.check(r.Context()); err != nil {
			return err
		}
		next, err := v.Uploads.Append(id, offset, data)
		if err != nil {
			return fail(err)
		}
		if err := v.check(r.Context()); err != nil {
			return err
		}
		return respond(http.StatusOK, map[string]int64{"offset": next})
	case r.Method == http.MethodPost && action == "complete":
		result, err := v.Uploads.Complete(id)
		if err != nil {
			return fail(err)
		}
		if err := v.check(r.Context()); err != nil {
			return err
		}
		result.ID = token
		result.Path = ""
		return respond(http.StatusOK, result)
	case r.Method == http.MethodDelete && action == "":
		if err := v.Uploads.Cancel(id); err != nil {
			return fail(err)
		}
		return respond(http.StatusOK, map[string]bool{"cancelled": true})
	}
	return respond(http.StatusNotFound, errorResponse{Error: "Not found"})
}
