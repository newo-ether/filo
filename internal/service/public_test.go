package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/uploads"
)

func TestPublicServerNeverAdmitsPlainBearerRequests(t *testing.T) {
	native := newFakeSessions(0)
	server, err := NewPublicServer(ServerOptions{Sessions: native, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/v1/sessions", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized || native.mutations != 0 {
		t.Fatal("plaintext public path was admitted")
	}
}

// The Android counterpart uses only this private test server and synthetic data.
// It is never a live native account, user's session, service installation or UI.
func TestEncryptedAttachmentFixture(t *testing.T) {
	ready := os.Getenv("FILO_ATTACHMENT_FIXTURE_READY")
	if ready == "" {
		t.Skip("opt-in Android interoperability fixture")
	}
	store, err := uploads.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	native := newFakeSessions(0)
	native.sessions = []protocol.Session{}
	handler, err := NewPublicServer(ServerOptions{Sessions: native, Token: testToken, Uploads: store})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	if err := os.WriteFile(ready, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("Android attachment round trip did not finish")
		case <-tick.C:
			native.mu.Lock()
			sends := append([]string(nil), native.sends...)
			native.mu.Unlock()
			if len(sends) == 0 {
				continue
			}
			if len(sends) != 1 {
				t.Fatal("native input repeated")
			}
			marker := "Attached files on this computer:\n"
			_, refs, ok := strings.Cut(sends[0], marker)
			if !ok {
				t.Fatal("missing native file references")
			}
			var files []struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(refs), &files) != nil || len(files) != 1 {
				t.Fatal("invalid native references")
			}
			raw, err := os.ReadFile(files[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			expected := make([]byte, 600003)
			for i := range expected {
				expected[i] = byte(i)
			}
			if !bytes.Equal(raw, expected) {
				t.Fatal("uploaded original bytes changed")
			}
			return
		}
	}
}
