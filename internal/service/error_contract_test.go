package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/newo-ether/filo/internal/apierror"
	"github.com/newo-ether/filo/internal/protocol"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failingErrorSessions struct{ *fakeSessions }

func (s *failingErrorSessions) Read(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
	return protocol.ConversationPage{}, errors.New("Original desktop owner is unavailable: " + testToken)
}
func (s *failingErrorSessions) Send(context.Context, string, string, string) (protocol.SendReceipt, error) {
	s.bump(&s.mutations)
	return protocol.SendReceipt{}, CodexRpcError("thread already has an active writer", -32600)
}
func TestHttpAndStreamShareUsefulErrorsWithoutRetryingNativeMutations(t *testing.T) {
	fake := &failingErrorSessions{newFakeSessions(1)}
	ts := newTestServer(t, ServerOptions{Sessions: fake, Events: NewHub()})
	for _, suffix := range []string{"", "/events"} {
		r := get(t, ts.URL+"/v1/sessions/"+payloadSession+suffix, authHeaders())
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if suffix != "" {
			if r.StatusCode != 200 || !strings.HasPrefix(text, "event: error\ndata: ") {
				t.Fatalf("stream: %d %s", r.StatusCode, text)
			}
			text = strings.TrimSpace(strings.TrimPrefix(text, "event: error\ndata: "))
		} else if r.StatusCode != 502 {
			t.Fatalf("HTTP status %d", r.StatusCode)
		}
		var failure apierror.Response
		if json.Unmarshal([]byte(text), &failure) != nil || failure.Code != "desktop_unavailable" ||
			failure.Message == "" || strings.Contains(failure.Error, testToken) {
			t.Fatalf("error: %+v", failure)
		}
	}
	r := post(t, ts.URL+"/v1/sessions/"+payloadSession+"/messages",
		`{"text":"once","clientId":"11111111-1111-4111-8111-999999999999"}`, authHeaders())
	var failure apierror.Response
	decode(t, r, &failure)
	if r.StatusCode != http.StatusConflict || failure.Code != "session_busy" || fake.mutations != 1 {
		t.Fatalf("mutation: %d %+v count=%d", r.StatusCode, failure, fake.mutations)
	}
}
