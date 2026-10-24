//go:build windows

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func qualifyDaemonHTTP(t *testing.T, surface *CLIControlledSessions,
	scope func(context.Context) (string, error), id string) {
	t.Helper()
	adapter := Adapter{ID: "codex-cli", Family: "codex", Origin: "cli", Scope: scope,
		Sessions: surface, Capabilities: surface.Capabilities(),
		Status: func() AdapterStatus { return AdapterStatus{State: "connected"} }}
	_, base, client := encryptedAdapterServer(t, []Adapter{adapter}, nil)
	client.Timeout = 30 * time.Second
	path := base + "/v1/adapters/codex-cli/sessions/" + id
	status, data := adapterRequest(t, client, http.MethodGet, path, nil, nil)
	var page protocol.ConversationPage
	if status != http.StatusOK || json.Unmarshal(data, &page) != nil || page.Access == nil {
		t.Fatalf("encrypted native conversation read status=%d", status)
	}
	clientID, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	status, data = adapterRequest(t, client, http.MethodPost, path+"/messages", map[string]any{
		"text":     "Reply exactly FILO_ENCRYPTED_NATIVE_OK. Do not use tools.",
		"clientId": clientID, "accessBinding": page.Access.Binding,
	}, nil)
	var receipt protocol.SendReceipt
	if status != http.StatusAccepted || json.Unmarshal(data, &receipt) != nil ||
		receipt.ClientID != clientID || receipt.TurnID == "" {
		t.Fatalf("encrypted native Send status=%d; input not replayed", status)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, data = adapterRequest(t, client, http.MethodGet, path, nil, nil)
		if status != http.StatusOK || json.Unmarshal(data, &page) != nil {
			t.Fatalf("encrypted native readback status=%d", status)
		}
		confirmed := false
		for _, input := range page.InputReceipts {
			if input.ClientID == clientID && input.TurnID == receipt.TurnID && input.MessageID != "" {
				confirmed = true
			}
		}
		runtime := page.Runtime
		if confirmed && runtime != nil && runtime.Status == "idle" && runtime.ActiveTurnID == nil &&
			runtime.CompletedTurnID.Known && runtime.CompletedTurnID.Value != nil &&
			*runtime.CompletedTurnID.Value == receipt.TurnID {
			t.Logf("encrypted public HTTP native client=%s turn=%s exact item and idle confirmed", clientID, receipt.TurnID)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("encrypted native receipt/terminal absent; input not replayed")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
