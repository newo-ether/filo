package uploads

import (
	"encoding/json"
	"errors"
	"github.com/newo-ether/filo/internal/apierror"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Handler is mounted only inside Filo's authenticated application transport.
// It must never be exposed as an independent unauthenticated listener.
type Handler struct{ Store *Store }

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	respond := func(status int, value any) { w.WriteHeader(status); json.NewEncoder(w).Encode(value) }
	fail := func(err error) {
		status := http.StatusBadRequest
		if errors.Is(err, ErrBusy) {
			status = http.StatusTooManyRequests
		}
		respond(status, apierror.New(status, err.Error(), ""))
	}
	if h.Store == nil {
		respond(http.StatusServiceUnavailable, apierror.New(http.StatusServiceUnavailable, "Attachment storage is unavailable", ""))
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/v1/uploads")
	switch {
	case r.Method == http.MethodPost && suffix == "":
		data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil || len(data) > 4096 {
			fail(ErrInvalid)
			return
		}
		var meta Metadata
		if json.Unmarshal(data, &meta) != nil {
			fail(ErrInvalid)
			return
		}
		result, err := h.Store.Begin(meta)
		if err != nil {
			fail(err)
			return
		}
		respond(http.StatusCreated, result)
	case r.Method == http.MethodPut && strings.Count(suffix, "/") == 1:
		id := strings.TrimPrefix(suffix, "/")
		offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil {
			fail(ErrInvalid)
			return
		}
		if r.ContentLength > MaxChunkBytes {
			respond(http.StatusRequestEntityTooLarge, apierror.New(http.StatusRequestEntityTooLarge, "Attachment chunk is too large", ""))
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, MaxChunkBytes+1))
		if err != nil || len(data) > MaxChunkBytes {
			fail(ErrInvalid)
			return
		}
		next, err := h.Store.Append(id, offset, data)
		if err != nil {
			fail(err)
			return
		}
		respond(http.StatusOK, map[string]int64{"offset": next})
	case r.Method == http.MethodPost && strings.HasSuffix(suffix, "/complete"):
		id := strings.TrimSuffix(strings.TrimPrefix(suffix, "/"), "/complete")
		result, err := h.Store.Complete(id)
		if err != nil {
			fail(err)
			return
		}
		respond(http.StatusOK, result)
	case r.Method == http.MethodDelete && strings.Count(suffix, "/") == 1:
		if err := h.Store.Cancel(strings.TrimPrefix(suffix, "/")); err != nil {
			fail(err)
			return
		}
		respond(http.StatusOK, map[string]bool{"cancelled": true})
	default:
		respond(http.StatusNotFound, apierror.New(http.StatusNotFound, "Not found", ""))
	}
}

// References passes raw target paths to Codex as user attachment references.
// No file content is extracted, converted, embedded in a prompt, or executed.
func References(text string, attachments []Metadata) string {
	if len(attachments) == 0 {
		return text
	}
	paths := make([]map[string]string, 0, len(attachments))
	for _, item := range attachments {
		paths = append(paths, map[string]string{"name": item.Name, "path": item.Path, "mime": item.MIME})
	}
	encoded, _ := json.Marshal(paths)
	return text + "\n\nAttached files on this computer:\n" + string(encoded)
}
