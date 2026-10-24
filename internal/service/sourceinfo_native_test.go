//go:build windows

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"testing"
	"time"

	"github.com/newo-ether/conch/encryptedhttp"
	"github.com/newo-ether/filo/internal/protocol"
)

// This opt-in probe reads native discovery only. It never creates native work.
func TestNativeSourceInfoReadiness(t *testing.T) {
	if os.Getenv("FILO_NATIVE_INFO_PROBE") != "1" {
		t.Skip("explicit native discovery probe is disabled")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	assembly, err := assembleUserSources(map[string]any{
		"sources":    []any{codexCLISource, claudeCodeSource, claudeDesktopSource},
		"desktopSid": account.Uid,
	}, t.TempDir(), testToken, func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer assembly.close()
	home := nativeHome(account.Uid)
	scope := codexSourceScope(account.Uid, home, StaticUserWorkerToken(testToken))
	desktop := Adapter{ID: codexDesktopSource, Family: "codex", Origin: "desktop", Scope: scope,
		Sessions: newFakeSessions(0), Capabilities: protocol.AdapterCapabilities{History: true},
		Status: func() AdapterStatus { return AdapterStatus{State: "unavailable"} }}
	assembly.adapters = append([]Adapter{desktop}, assembly.adapters...)
	private, err := NewServer(ServerOptions{Token: testToken, Sessions: newFakeSessions(0),
		Identity: &ServiceIdentity{SessionMode: "standalone"}, Adapters: assembly.adapters})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(private)
	defer endpoint.Close()
	worker, err := NewUserWorker(endpoint.URL, StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	adapters := gatewaySourceAdapters([]string{codexDesktopSource, codexCLISource, claudeCodeSource, claudeDesktopSource}, worker, func(string, string) {}, desktop, nil)
	public, err := NewPublicServer(ServerOptions{Token: testToken, Sessions: newFakeSessions(0), Adapters: adapters})
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(public)
	defer gateway.Close()
	client := &http.Client{Timeout: 15 * time.Second, Transport: &encryptedhttp.Transport{Key: []byte(testToken)}}
	started := time.Now()
	response, err := client.Get(gateway.URL + "/v1/info")
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("encrypted source info after %s: %v", elapsed, err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	t.Logf("native source info status=%d duration=%s", response.StatusCode, elapsed)
	if response.StatusCode != http.StatusOK || elapsed >= 2*time.Second {
		t.Fatal("native discovery exceeds the installed readiness request budget")
	}
}
