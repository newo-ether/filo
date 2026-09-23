package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/conch/encryptedhttp"
)

func TestInstallerRequestVerifiesEncryptedResponseAndNeverLeaksCredential(t *testing.T) {
	directory := t.TempDir()
	token := filepath.Join(directory, "token")
	if err := os.WriteFile(token, []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	gateway, _ := encryptedhttp.New([]byte(testToken), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(409)
		_, _ = io.WriteString(w, `{"error":"fixture conflict"}`)
	}))
	server := httptest.NewServer(gateway)
	defer server.Close()
	var output, diagnostic bytes.Buffer
	code := RunRequest([]string{"-url", server.URL, "-token-file", token,
		"-method", "POST", "-path", "/v1/sessions", "-timeout", "2s"}, &output, &diagnostic)
	if code != 1 || calls.Load() != 1 || output.String() != `{"error":"fixture conflict"}` {
		t.Fatalf("request outcome %d/%d/%s", code, calls.Load(), output.String())
	}
	if bytes.Contains(output.Bytes(), []byte(testToken)) || bytes.Contains(diagnostic.Bytes(), []byte(testToken)) {
		t.Fatal("credential escaped")
	}
}
