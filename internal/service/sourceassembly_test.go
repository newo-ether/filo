package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/conch/encryptedhttp"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/uploads"
)

func TestAssemblyCodexIdentityMatchesDesktopCanonicalRule(t *testing.T) {
	data, _ := json.Marshal([3]string{"codex", "scope-a", payloadSession})
	digest := sha256.Sum256(data)
	access := codexReadIdentity("scope-a", payloadSession)
	if access.CanonicalSessionRef != base64.RawURLEncoding.EncodeToString(digest[:]) ||
		access.NativeScopeID != "scope-a" || access.Binding == "" || access.Capabilities.IdleSend {
		t.Fatalf("unexpected canonical access: %+v", access)
	}
	if access.CanonicalSessionRef == codexReadIdentity("scope-b", payloadSession).CanonicalSessionRef {
		t.Fatal("different account scopes shared canonical identity")
	}
}

func TestAssemblyClaudeCredentialIsDistinctStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-mod-token")
	first, err := claudeModToken(path, testToken)
	if err != nil || first == testToken || !tokenPattern.MatchString(first) {
		t.Fatalf("secret: %v", err)
	}
	second, err := claudeModToken(path, testToken)
	if err != nil || second != first {
		t.Fatal("existing private secret changed")
	}
	if err := os.WriteFile(path, []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeModToken(path, testToken); err == nil {
		t.Fatal("worker secret reused for Mod")
	}
}

func TestAssemblyDesktopDefaultDoesNotAdmitOtherSources(t *testing.T) {
	assembly, err := assembleUserSources(map[string]any{}, t.TempDir(), testToken, func(string, string) {})
	if err != nil || len(assembly.adapters) != 0 || assembly.bridge != nil || assembly.cli != nil {
		t.Fatalf("legacy sources created extra transports: %+v %v", assembly, err)
	}
	assembly.close()
	for _, sid := range []string{"S-1-5-18", "S-1-5-21-1-2-3-987654321"} {
		_, err := assembleUserSources(map[string]any{"sources": []any{codexCLISource}, "desktopSid": sid},
			t.TempDir(), testToken, func(string, string) {})
		if err == nil {
			t.Fatal("unverified native user admitted")
		}
	}
}

func TestAssemblySourcePortsRemainDistinct(t *testing.T) {
	for _, port := range []float64{7437, 23456} {
		config := map[string]any{"claudeBridgePort": port}
		if err := validateSourceEndpoints(config, []string{claudeCodeSource}, 23456); err == nil {
			t.Fatal("bridge shared private worker endpoint")
		}
	}
}

type assemblyReadFixture struct {
	*fakeSessions
	scope         *string
	identityReads int
	change        bool
}

func (f *assemblyReadFixture) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	page := protocol.SessionPage{Sessions: []protocol.Session{}}
	for i := 0; i < 30; i++ {
		scope := cachedSourceReadScope(ctx)
		if scope == "" {
			f.identityReads++
		}
		id := "session-" + strconv.Itoa(i)
		access := codexReadIdentity(scope, id)
		page.Sessions = append(page.Sessions, protocol.Session{ID: id, Access: &access})
	}
	if f.change {
		*f.scope = "account-b"
	}
	return page, nil
}

func (f *assemblyReadFixture) Access(context.Context, string) (protocol.SessionAccess, error) {
	return protocol.SessionAccess{}, nil
}

func (f *assemblyReadFixture) ValidateAccess(context.Context, string, string) error {
	return sourceUnsupported()
}

func TestAssemblyCatalogUsesOneCapturedScopeAndRejectsAccountSwap(t *testing.T) {
	scope, reads := "account-a", 0
	fixture := &assemblyReadFixture{fakeSessions: newFakeSessions(0), scope: &scope}
	reader := &sourceReader{SessionAPI: fixture, scope: func(context.Context) (string, error) {
		reads++
		return scope, nil
	}}
	page, err := reader.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 30 || reads != 2 || fixture.identityReads != 0 {
		t.Fatalf("catalog verification repeated per row: rows=%d scopeReads=%d identityReads=%d err=%v", len(page.Sessions), reads, fixture.identityReads, err)
	}
	fixture.change = true
	if page, err := reader.List(context.Background(), ""); err == nil || len(page.Sessions) != 0 {
		t.Fatal("old-account catalog escaped after account change")
	}
}

func TestAssemblyGatewaySourcesUsePrivateMetadataWithoutPerRowInfo(t *testing.T) {
	var mu sync.Mutex
	scope, infoReads := "scope-a", 0
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/info":
			infoReads++
			_ = json.NewEncoder(w).Encode(map[string]any{"extensions": map[string]int{"multiAdapter": 1},
				"adapters": []protocol.AdapterDescriptor{{ID: codexCLISource, NativeScopeID: scope, State: "connected"}}})
		case "/v1/adapters/codex-cli/sessions":
			page := protocol.SessionPage{Sessions: []protocol.Session{}}
			for i := 0; i < 30; i++ {
				id := "session-" + strconv.Itoa(i)
				access := codexReadIdentity(scope, id)
				page.Sessions = append(page.Sessions, protocol.Session{ID: id, Access: &access})
			}
			_ = json.NewEncoder(w).Encode(page)
		default:
			w.WriteHeader(404)
		}
	}))
	defer private.Close()
	worker, err := NewUserWorker(private.URL, StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	adapters := gatewaySourceAdapters([]string{codexCLISource}, worker, func(string, string) {}, Adapter{}, nil)
	if len(adapters) != 1 || worker.NewTasks() != nil || adapters[0].CapabilitySource == nil {
		t.Fatal("CLI source did not retain an independent fresh private capability owner")
	}
	source := adapters[0].Sessions.(*userSource)
	if _, err := source.scope(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := source.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Sessions {
		if _, err := source.CatalogAccess(context.Background(), row.ID); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	reads := infoReads
	scope = "scope-b"
	mu.Unlock()
	if reads != 1 {
		t.Fatalf("catalog queried private info %d times", reads)
	}
	if _, err := source.scope(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	count := len(source.access)
	source.mu.Unlock()
	if count != 0 {
		t.Fatal("old account access cache survived fresh descriptor")
	}
}

func TestAssemblyAttachmentsReachControlledSources(t *testing.T) {
	store, err := uploads.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker, err := NewUserWorker("http://127.0.0.1:1", StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	ids := []string{codexCLISource, claudeCodeSource, claudeDesktopSource}
	for _, adapter := range gatewaySourceAdapters(ids, worker, func(string, string) {}, Adapter{}, store) {
		controlled := adapter.ID == codexCLISource || adapter.ID == claudeCodeSource
		if adapter.Capabilities.Uploads != controlled || (adapter.Uploads == store) != controlled {
			t.Fatalf("%s uploads=%v store=%v", adapter.ID, adapter.Capabilities.Uploads, adapter.Uploads != nil)
		}
		if adapter.Sessions.(*userSource).attachments != controlled {
			t.Fatalf("%s private source attachment projection mismatch", adapter.ID)
		}
	}
}

func TestAssemblyEncryptedGatewayWithoutDesktopDoesNotConnectAuxiliary(t *testing.T) {
	directory := t.TempDir()
	port := reserveLoopbackPort(t)
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"extensions": map[string]int{"multiAdapter": 1},
			"adapters": []protocol.AdapterDescriptor{{ID: claudeCodeSource, Family: "claude", Origin: "cli", NativeScopeID: "isolated", State: "unavailable"}}})
	}))
	defer private.Close()
	workerToken := filepath.Join(directory, "worker-token")
	writeEntryFixtureFile(t, workerToken, testToken)
	writeEntryFixtureFile(t, filepath.Join(directory, entryTokenName), testToken)
	writeEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(Marshal(map[string]any{
		"bind": "127.0.0.1", "port": port, "desktopSid": "S-1-5-21-1-2-3-1001",
		"sources": []string{claudeCodeSource}, "workerUrl": private.URL, "workerTokenPath": workerToken,
		"appServerUrl": "invalid-and-must-never-be-opened",
	})))
	done := make(chan int, 1)
	go func() {
		done <- runSystemService(systemServiceOptions{Directory: directory, Output: io.Discard, Error: io.Discard})
	}()
	client := &http.Client{Timeout: time.Second, Transport: &encryptedhttp.Transport{Key: []byte(testToken)}}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	var info map[string]any
	for i := 0; i < 80; i++ {
		assertEntryRunning(t, done)
		if value, ok := readEntryInfo(client, base, authHeaders()); ok {
			info = value
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if info == nil {
		t.Fatal("encrypted multi-source entry did not start")
	}
	if _, ok := info["desktop"]; ok {
		t.Fatal("Claude-only gateway constructed Desktop status")
	}
	if info["sessionMode"] == "existing" {
		t.Fatal("Claude-only gateway advertised legacy Codex")
	}
	response, err := client.Post(base+"/v1/shutdown", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if code := awaitEntryExit(t, done); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(directory, "uploads")); err != nil {
		t.Fatal("Claude-only gateway did not create its upload store", err)
	}
	if _, err := os.Stat(filepath.Join(directory, serviceTaskDirectoryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Claude-only gateway created Desktop task ownership")
	}
}
