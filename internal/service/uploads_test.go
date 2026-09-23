package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/uploads"
)

func TestRawAttachmentsAuthenticateBeforeStorageAndSendOnlyAfterCompletion(t *testing.T) {
	store, err := uploads.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	native := newFakeSessions(0)
	server, err := NewServer(ServerOptions{Sessions: native, Token: testToken, Uploads: store})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{0, 255, 128, 13, 10}
	sum := sha256.Sum256(data)
	body, _ := json.Marshal(uploads.Metadata{Name: "raw.png", MIME: "image/png", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	request := func(method, path string, data []byte, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		if auth {
			r.Header.Set("Authorization", "Bearer "+testToken)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/v1/uploads", body, false); w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
	w := request("POST", "/v1/uploads", body, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var pending uploads.Metadata
	json.Unmarshal(w.Body.Bytes(), &pending)
	send, _ := json.Marshal(map[string]any{"text": "", "clientId": payloadSession, "attachments": []string{pending.ID}})
	if w := request("POST", "/v1/sessions/"+payloadSession+"/messages", send, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if native.mutations != 0 {
		t.Fatal("incomplete attachment reached native execution")
	}
	if w := request("PUT", "/v1/uploads/"+pending.ID+"?offset=0", data, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/uploads/"+pending.ID+"/complete", nil, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/v1/sessions/"+payloadSession+"/messages", send, true); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(native.sends) != 1 || !strings.Contains(native.sends[0], "raw.png") || strings.Contains(native.sends[0], string(data)) {
		t.Fatal("native received extracted bytes or no file reference")
	}
}
