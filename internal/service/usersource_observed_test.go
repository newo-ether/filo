package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestUserSourceForwardsOnlyClaudeNonExactReceipts(t *testing.T) {
	for _, sourceID := range []string{claudeCodeSource, claudeDesktopSource, codexCLISource} {
		for _, state := range []string{"submitted", "unconfirmed", "observed", "observed-without-identity"} {
			t.Run(sourceID+"/"+state, func(t *testing.T) {
				chain := newSourceMutationChain(t, sourceID)
				binding := fixtureBinding(t, chain.client, chain.publicURL, sourceID)
				receipt := protocol.SendReceipt{ClientID: payloadSession, Kind: "submitted_observed", State: state}
				if state == "observed" {
					receipt.MessageID = "native-user"
				}
				if state == "observed-without-identity" {
					receipt.State = "observed"
				}
				chain.setRewrite(func(r *http.Request, response *httptest.ResponseRecorder) {
					if r.Method == http.MethodPost && response.Code < 300 && strings.HasSuffix(r.URL.Path, "/messages") {
						response.Body.Reset()
						response.Header().Del("Content-Length")
						_ = json.NewEncoder(response.Body).Encode(receipt)
					}
				})
				path := chain.publicURL + "/v1/adapters/" + sourceID + "/sessions/" + payloadSession + "/messages"
				status, body := adapterRequest(t, chain.client, "POST", path,
					map[string]any{"text": "one input", "clientId": payloadSession, "accessBinding": binding}, nil)
				accepted := sourceID != codexCLISource && state != "observed-without-identity"
				if accepted {
					var actual protocol.SendReceipt
					if status != http.StatusAccepted || json.Unmarshal(body, &actual) != nil || actual != receipt {
						t.Fatalf("non-exact receipt lost: status=%d actual=%+v body=%s", status, actual, body)
					}
				} else if status < 400 {
					t.Fatal("invalid or non-Claude non-exact receipt accepted")
				}
				if len(chain.writes()) != 1 || chain.native.dispatches != 1 || chain.other.dispatches != 0 {
					t.Fatal("input replayed or routed to another source")
				}
			})
		}
	}
}
