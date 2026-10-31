package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func desktopHistoryIdentity(t *testing.T) string {
	t.Helper()
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	host := "local_" + fixtureOther
	path := filepath.Join(local, "Claude-3p", "claude-code-sessions", "user", "account", host+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"sessionId": host, "cliSessionId": fixtureSession, "title": "Native title"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDesktopHistoryWithoutClassicMetadataUsesExactNativeIdentity(t *testing.T) {
	a, nonce, path := transcriptFixture(t, "desktop", 3)
	desktopHistoryIdentity(t)
	a.mu.Lock()
	a.registrations[fixtureSession].transcript = ""
	a.mu.Unlock()
	before, _ := os.ReadFile(path)
	page, err := a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("native history missing without classic event: rows=%d err=%v", len(page.Messages), err)
	}
	if page.Access.Binding != nonce || page.Runtime.Status != "unknown" || page.Runtime.ActiveTurnID != nil {
		t.Fatal("history changed native authority or inferred lifecycle")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("history discovery wrote the native transcript")
	}
}

func TestDesktopHistoryDiscoveryKeepsPagingAndRejectsChangedRegistration(t *testing.T) {
	a, nonce, _ := transcriptFixture(t, "desktop", 300)
	desktopHistoryIdentity(t)
	a.mu.Lock()
	a.registrations[fixtureSession].transcript = ""
	a.mu.Unlock()
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	var oldCursor string
	for {
		page, err := a.Read(context.Background(), fixtureSession, cursor, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Access.Binding != nonce || page.Runtime.Status != "unknown" {
			t.Fatal("discovered history changed native authority")
		}
		for _, row := range page.Messages {
			if seen[row.ID] {
				t.Fatal("discovery duplicated a native row across pages")
			}
			seen[row.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		if pages > 4 || *page.NextCursor == cursor {
			t.Fatal("discovered history did not make bounded progress")
		}
		cursor = *page.NextCursor
		oldCursor = cursor
	}
	if len(seen) != 300 || pages != 3 {
		t.Fatalf("discovered rows=%d pages=%d", len(seen), pages)
	}
	status, replacement := bridgeCall(t, a, "register", registerRequest{SessionID: fixtureSession,
		Instance: "test-instance", Origin: "desktop", Version: "fixture"})
	if status != http.StatusOK || replacement.Nonce == nonce {
		t.Fatal("fixture did not replace native registration")
	}
	if _, err := a.Read(context.Background(), fixtureSession, oldCursor, true, nil); err == nil {
		t.Fatal("discovery admitted a cursor from a replaced registration")
	}
}

func TestDesktopHistoryDiscoveryRejectsCancelledAndOversizedStores(t *testing.T) {
	a, _, path := transcriptFixture(t, "desktop", 1)
	desktopHistoryIdentity(t)
	a.mu.Lock()
	a.registrations[fixtureSession].transcript = ""
	a.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.discoverDesktopTranscript(ctx, fixtureSession)
	a.mu.Lock()
	found := a.registrations[fixtureSession].transcript
	a.mu.Unlock()
	if found != "" {
		t.Fatal("cancelled discovery published a transcript")
	}
	root := filepath.Dir(filepath.Dir(path))
	for index := 0; index < 1024; index++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("extra-%04d", index)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if found := desktopTranscriptPath(context.Background(), fixtureSession); found != "" {
		t.Fatal("discovery accepted an oversized native store")
	}
}

func TestDesktopHistoryDiscoveryRefusesMissingAmbiguousOrForeignIdentity(t *testing.T) {
	for _, scenario := range []string{"missing identity", "wrong engine", "ambiguous transcript", "terminal source"} {
		t.Run(scenario, func(t *testing.T) {
			origin := "desktop"
			if scenario == "terminal source" {
				origin = "terminal"
			}
			a, nonce, path := transcriptFixture(t, origin, 3)
			identity := desktopHistoryIdentity(t)
			switch scenario {
			case "missing identity":
				if err := os.Remove(identity); err != nil {
					t.Fatal(err)
				}
			case "wrong engine":
				if err := os.WriteFile(identity, []byte(`{"sessionId":"local_`+fixtureOther+`","cliSessionId":"`+fixtureOther+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "ambiguous transcript":
				other := filepath.Join(filepath.Dir(filepath.Dir(path)), "other", fixtureSession+".jsonl")
				if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
					t.Fatal(err)
				}
				data, _ := os.ReadFile(path)
				if err := os.WriteFile(other, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			a.mu.Lock()
			a.registrations[fixtureSession].transcript = ""
			a.mu.Unlock()
			status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
				Events: []modEvent{{Kind: "row", UUID: fixtureOther, Role: "user", Text: "live only"}}})
			if status != http.StatusOK {
				t.Fatal(status)
			}
			page, err := a.Read(context.Background(), fixtureSession, "", true, nil)
			if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "live only" {
				t.Fatalf("unqualified history exposed or live rows lost: rows=%d err=%v", len(page.Messages), err)
			}
		})
	}
}
