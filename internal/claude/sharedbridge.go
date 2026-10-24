package claude

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// SharedBridge serves selected native surfaces on one private listener. Each
// adapter retains its own registrations, scope, status and observation budget.
// Envelopes carry the registration nonce, so identical native session IDs on
// different surfaces never select each other's registration.
func SharedBridge(adapters ...*Adapter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if len(adapters) == 0 {
			http.NotFound(w, request)
			return
		}
		data, err := io.ReadAll(io.LimitReader(request.Body, maxBody+1))
		if err != nil || len(data) > maxBody {
			adapters[0].respond(w, http.StatusBadRequest, bridgeResponse{Error: "Invalid bounded body"})
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(data))
		var identity struct {
			Origin    string `json:"origin"`
			SessionID string `json:"sessionId"`
			Nonce     string `json:"nonce"`
		}
		selected := adapters[0]
		if json.Unmarshal(data, &identity) == nil {
			for _, adapter := range adapters {
				if request.URL.Path == bridgePrefix+"register" {
					if identity.Origin == adapter.origin {
						selected = adapter
						break
					}
					continue
				}
				adapter.mu.Lock()
				registration, err := adapter.findLocked(identity.SessionID)
				match := err == nil && identity.Nonce == registration.nonce
				adapter.mu.Unlock()
				if match {
					selected = adapter
					break
				}
			}
		}
		// The native adapter still owns authentication and full strict decoding.
		selected.ServeHTTP(w, request)
	})
}
