package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/uploads"
)

type uploadGrantNative struct {
	*sourceMutationNative
	granted bool
}

func (f *uploadGrantNative) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	access, err := f.sourceMutationNative.Access(ctx, id)
	access.Capabilities.Uploads = f.granted
	return access, err
}

func TestUserSourceUploadsRequireNativeGrantAndPublicStore(t *testing.T) {
	for _, test := range []struct {
		name                             string
		sourceGrant, sessionGrant, store bool
	}{
		{"granted", true, true, true},
		{"legacy-session", true, false, true},
		{"legacy-source", false, false, true},
		{"no-public-store", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			chain := newSourceMutationChain(t, claudeCodeSource)
			private := chain.privateRoot.adapters[claudeCodeSource]
			private.Capabilities.Uploads = test.sourceGrant
			private.Sessions = &uploadGrantNative{chain.native, test.sessionGrant}
			// Reconfigure through the production owner to exercise layer narrowing.
			chain.privateRoot.adapterOrder = nil
			if err := chain.privateRoot.configureAdapters([]Adapter{private.Adapter}); err != nil {
				t.Fatal(err)
			}
			private = chain.privateRoot.adapters[claudeCodeSource]
			if private.Capabilities.Uploads != test.sourceGrant {
				t.Fatal("worker erased native permission because it has no upload store")
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/adapters/claude-code/uploads", strings.NewReader("{}"))
			request.Header.Set("Authorization", "Bearer "+strings.Repeat("b", 64))
			recorder := httptest.NewRecorder()
			chain.privateRoot.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotImplemented {
				t.Fatalf("worker without storage accepted upload route: %d", recorder.Code)
			}

			var store *uploads.Store
			if test.store {
				var err error
				store, err = uploads.New(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
			}
			chain.source.attachments = test.store
			adapter := Adapter{ID: claudeCodeSource, Family: "claude", Origin: "cli", Scope: chain.source.scope,
				Sessions: chain.source, Capabilities: fixtureCaps(), CapabilitySource: chain.source.capabilities, Uploads: store}
			_, base, client := encryptedAdapterServer(t, []Adapter{adapter}, nil)
			status, data := adapterRequest(t, client, http.MethodGet, base+"/v1/info", nil, nil)
			var info infoResponse
			if status != http.StatusOK || json.Unmarshal(data, &info) != nil || len(info.Adapters) != 1 {
				t.Fatalf("public discovery: %d", status)
			}
			wantSource := test.sourceGrant && test.store
			if info.Adapters[0].Capabilities.Uploads != wantSource {
				t.Fatal("public upload capability ignored native grant or local storage")
			}
			path := base + "/v1/adapters/claude-code"
			status, data = adapterRequest(t, client, http.MethodGet, path+"/sessions/"+payloadSession, nil, nil)
			var page protocol.ConversationPage
			if status != http.StatusOK || json.Unmarshal(data, &page) != nil || page.Access == nil {
				t.Fatalf("public session: %d", status)
			}
			wantSession := wantSource && test.sessionGrant
			if page.Access.Capabilities.Uploads != wantSession {
				t.Fatal("public session upload permission exceeded its native grant")
			}
			digest := sha256.Sum256([]byte("hello"))
			meta := map[string]any{"name": "fixture.txt", "mime": "text/plain", "size": 5,
				"sha256": hex.EncodeToString(digest[:]), "nativeScopeId": "scope_a"}
			status, data = adapterRequest(t, client, http.MethodPost, path+"/uploads", meta, nil)
			if !wantSource {
				if status != http.StatusNotImplemented || chain.native.dispatches != 0 {
					t.Fatal("unavailable uploads reached storage or native execution")
				}
				return
			}
			var pending uploads.Metadata
			if status != http.StatusCreated || json.Unmarshal(data, &pending) != nil || pending.ID == "" {
				t.Fatalf("upload begin: %d", status)
			}
			uploadPath := path + "/uploads/" + url.PathEscape(pending.ID)
			status, _ = adapterRequest(t, client, http.MethodPut, uploadPath+"?offset=0", []byte("hello"), nil)
			if status != http.StatusOK {
				t.Fatalf("upload chunk: %d", status)
			}
			status, _ = adapterRequest(t, client, http.MethodPost, uploadPath+"/complete", nil, nil)
			if status != http.StatusOK {
				t.Fatalf("upload complete: %d", status)
			}
			body := map[string]any{"text": "read attachment", "clientId": payloadSession,
				"attachments": []string{pending.ID}, "accessBinding": page.Access.Binding}
			status, _ = adapterRequest(t, client, http.MethodPost, path+"/sessions/"+payloadSession+"/messages", body, nil)
			if wantSession {
				if status != http.StatusAccepted || chain.native.dispatches != 1 {
					t.Fatalf("granted upload dispatch: status=%d calls=%d", status, chain.native.dispatches)
				}
			} else if status < 400 || chain.native.dispatches != 0 {
				t.Fatal("legacy session gained attachment access from the new source grant")
			}
		})
	}
}
