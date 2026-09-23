package service

import (
	"context"
	"github.com/newo-ether/filo/internal/apierror"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

const streamHeartbeat = 5 * time.Second

// streamPage reads one bounded snapshot: the paged conversation a mobile view
// consumes, or the plain metadata page.
func (s *Server) streamPage(ctx context.Context, id string, payloads *ConversationPayloads,
	withBodies bool) (any, error) {
	if payloads == nil {
		return ReadConversationPage(ctx, s.sessions, id, "", PageOptions{Activity: true, Layer: s.layer})
	}
	if withBodies {
		return payloads.Page(ctx, s.sessions, id, "", s.layer, nil)
	}
	return payloads.Topology(ctx, s.sessions, id, "", s.layer)
}

// streamNotLoaded reports a runtime that still awaits its first native state;
// the heartbeat keeps refreshing until it arrives.
func streamNotLoaded(page any) bool {
	runtime := pageRuntime(page)
	return runtime != nil && runtime.Status == "notLoaded"
}

func pageRuntime(page any) *protocol.Runtime {
	switch value := page.(type) {
	case protocol.ConversationPage:
		return value.Runtime
	case protocol.TopologyPage:
		return value.Runtime
	}
	return nil
}

// stream serves one serialized snapshot per change. Native events coalesce, so
// a burst of changes costs one bounded read, and a change for another session
// never disturbs this stream.
func (s *Server) stream(ctx context.Context, w http.ResponseWriter, id string,
	payloads *ConversationPayloads, withBodies bool) error {
	release, err := s.sessions.Attach(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil
	}
	subscription := s.events.Subscribe(id)
	defer subscription.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	flush()
	previous := ""
	awaitingNativeState := false
	refresh := func() error {
		page, err := s.streamPage(ctx, id, payloads, withBodies)
		if err != nil {
			s.writeStreamError(w, err)
			flush()
			return err
		}
		text, err := EncodeResponse(page)
		if err != nil {
			s.writeStreamError(w, err)
			flush()
			return err
		}
		awaitingNativeState = streamNotLoaded(page)
		if text != previous {
			if _, err := io.WriteString(w, "data: "+text+"\n\n"); err != nil {
				return err
			}
			previous = text
			flush()
		}
		return nil
	}
	if refresh() != nil {
		return nil
	}
	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-subscription.Closed():
			return nil
		case <-subscription.Signal():
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return err
			}
			flush()
			if !awaitingNativeState {
				continue
			}
		}
		if refresh() != nil {
			return nil
		}
	}
}

func (s *Server) writeStreamError(w io.Writer, err error) {
	value := apierror.New(http.StatusBadGateway, err.Error(), strings.TrimPrefix(s.expected, "Bearer "))
	data, encodeErr := EncodeResponse(value)
	if encodeErr == nil {
		_, _ = io.WriteString(w, "event: error\ndata: "+data+"\n\n")
	}
}
