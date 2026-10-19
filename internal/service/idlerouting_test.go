package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/newo-ether/filo/internal/sessions"
)

func TestIdleRecoveryMarkerIsPrivateExactAndMutationScoped(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	for _, test := range []struct {
		name, layer, method, action, marker, remote, local string
		allowed                                            bool
	}{
		{"private send", "worker", "POST", "messages", id, "127.0.0.1:1", "127.0.0.1", true},
		{"private settings", "worker", "POST", "settings", id, "127.0.0.1:1", "127.0.0.1", true},
		{"private model", "worker", "POST", "model", id, "127.0.0.1:1", "127.0.0.1", true},
		{"gateway ignores marker", "desktop", "POST", "messages", id, "127.0.0.1:1", "127.0.0.1", false},
		{"read ignores marker", "worker", "GET", "messages", id, "127.0.0.1:1", "127.0.0.1", false},
		{"events ignore marker", "worker", "GET", "events", id, "127.0.0.1:1", "127.0.0.1", false},
		{"stop cannot admit", "worker", "POST", "stop", id, "127.0.0.1:1", "127.0.0.1", false},
		{"archive cannot admit", "worker", "POST", "archive", id, "127.0.0.1:1", "127.0.0.1", false},
		{"wrong task", "worker", "POST", "messages", "different", "127.0.0.1:1", "127.0.0.1", false},
		{"no marker", "worker", "POST", "messages", "", "127.0.0.1:1", "127.0.0.1", false},
		{"remote peer", "worker", "POST", "messages", id, "192.0.2.1:1", "127.0.0.1", false},
		{"public listener", "worker", "POST", "messages", id, "127.0.0.1:1", "0.0.0.0", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/v1/sessions/"+id+"/"+test.action, nil)
			request.RemoteAddr = test.remote
			request.Header.Set("X-Filo-Idle-Recovery", test.marker)
			request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey,
				&net.TCPAddr{IP: net.ParseIP(test.local), Port: 7437}))
			ctx := (&Server{layer: test.layer}).mutationContext(request, id, test.action)
			if actual := sessions.IdleRecoveryAllowed(ctx, id); actual != test.allowed {
				t.Fatalf("recovery admission = %v, want %v", actual, test.allowed)
			}
		})
	}
}
