package uploads

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPChunkReceiptAndRawReference(t *testing.T) {
	s := store(t)
	h := Handler{Store: s}
	data := []byte("\x00raw\xff\n")
	body, _ := json.Marshal(metadata(data))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/uploads", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var meta Metadata
	json.Unmarshal(w.Body.Bytes(), &meta)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("PUT", "/v1/uploads/"+meta.ID+"?offset=0", bytes.NewReader(data)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/uploads/"+meta.ID+"/complete", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt Metadata
	json.Unmarshal(w.Body.Bytes(), &receipt)
	text := References("inspect", []Metadata{receipt})
	if !strings.HasPrefix(text, "inspect\n\n") || strings.Contains(text, string(data)) {
		t.Fatal("raw file content entered text")
	}
	if !strings.Contains(text, "Attached files on this computer") {
		t.Fatal("missing target reference")
	}
}

func TestHTTPRejectsExcessBytesWithoutMutation(t *testing.T) {
	s := store(t)
	h := Handler{Store: s}
	for _, path := range []string{"/v1/uploads", "/v1/uploads/fake?offset=0"} {
		method := http.MethodPost
		if strings.Contains(path, "fake") {
			method = http.MethodPut
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(strings.Repeat("x", MaxChunkBytes+1))))
		if w.Code != 400 && w.Code != 413 {
			t.Fatal(w.Code)
		}
	}
	if len(s.pending) != 0 {
		t.Fatal("oversize body created upload")
	}
}
