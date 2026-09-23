package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShutdownAcknowledgementSurvivesImmediateConnectionClose(t *testing.T) {
	var server *httptest.Server
	handler, err := NewServer(ServerOptions{
		Token:      testToken,
		Sessions:   newFakeSessions(1),
		OnShutdown: func() { server.CloseClientConnections() },
	})
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(handler)
	defer server.Close()
	response := post(t, server.URL+"/v1/shutdown", "", authHeaders())
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("shutdown acknowledgement was truncated: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "{\"stopped\":true}" {
		t.Fatalf("shutdown response: %d %s", response.StatusCode, body)
	}
}
