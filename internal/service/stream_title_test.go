package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestIdleNativeTitleChangeStreamsOnHeartbeatWithoutNotification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := newFakeSessions(1)
		title := protocol.Text("Generated native title")
		source.page.Runtime = &protocol.Runtime{Status: "idle", Title: &title}
		server := &Server{events: NewHub(), sessions: source}
		defer server.events.Close()
		writer := httptest.NewRecorder()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- server.stream(ctx, writer, "session", nil, false) }()
		synctest.Wait()
		if !strings.Contains(writer.Body.String(), "Generated native title") {
			t.Fatal(writer.Body.String())
		}
		renamed := protocol.Text("Renamed while idle")
		source.mu.Lock()
		source.page.Runtime = &protocol.Runtime{Status: "idle", Title: &renamed}
		source.mu.Unlock()
		time.Sleep(streamHeartbeat)
		synctest.Wait()
		if !strings.Contains(writer.Body.String(), "Renamed while idle") {
			t.Fatal("idle rename required a native event", writer.Body.String())
		}
		cancel()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if source.attaches != 1 || source.releases != 1 {
			t.Fatal("stream leaked lease")
		}
	})
}
