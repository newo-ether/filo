//go:build windows

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAssemblySelectedClaudeOriginsUseOneBridgeAndSeparateState(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	if !systemServiceSIDPattern.MatchString(sid) {
		t.Skip("fixture requires an ordinary Windows account")
	}
	for _, selected := range [][]string{{claudeCodeSource}, {claudeDesktopSource}, {claudeCodeSource, claudeDesktopSource}} {
		t.Run(strings.Join(selected, "+"), func(t *testing.T) {
			sources := make([]any, len(selected))
			for i, id := range selected {
				sources[i] = id
			}
			config := map[string]any{"sources": sources, "desktopSid": sid}
			directory := t.TempDir()
			var changed []string
			assembly, err := assembleUserSources(config, directory, testToken, func(source, session string) {
				changed = append(changed, source)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer assembly.close()
			if assembly.bridge == nil || assembly.bridgeAddress != "127.0.0.1:7438" || len(assembly.adapters) != len(selected) || assembly.cli != nil {
				t.Fatal("selected Claude sources did not share a private listener")
			}
			secret, err := readEntryText(filepath.Join(directory, "claude-mod-token"))
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]Adapter{}
			for _, adapter := range assembly.adapters {
				ids[adapter.ID] = adapter
			}
			for _, origin := range []string{"terminal", "desktop"} {
				id := claudeCodeSource
				if origin == "desktop" {
					id = claudeDesktopSource
				}
				request := httptest.NewRequest(http.MethodPost, "/claude-mod/v1/register", strings.NewReader(
					`{"sessionId":"11111111-1111-4111-8111-111111111111","instance":"fixture","origin":"`+origin+`"}`))
				request.RemoteAddr = "127.0.0.1:54321"
				request.Header.Set("Authorization", "Bearer "+secret)
				writer := httptest.NewRecorder()
				assembly.bridge.ServeHTTP(writer, request)
				adapter, exists := ids[id]
				if !exists {
					if writer.Code != http.StatusForbidden {
						t.Fatalf("unselected %s registered: %d", id, writer.Code)
					}
					continue
				}
				if writer.Code != http.StatusOK || adapter.Status().State != "connected" || len(changed) == 0 || changed[len(changed)-1] != id {
					t.Fatalf("selected %s registration/status/change routing: %d %+v", id, writer.Code, changed)
				}
				for otherID, other := range ids {
					if otherID != id && origin == "terminal" && other.Status().State == "connected" {
						t.Fatal("terminal made Desktop connected")
					}
				}
			}
			server, err := NewServer(ServerOptions{Token: testToken, Adapters: assembly.adapters})
			if err != nil {
				t.Fatal(err)
			}
			for id, adapter := range ids {
				page, err := adapter.Sessions.List(context.Background(), "")
				if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Access == nil || page.Sessions[0].Access.NativeScopeID != adapter.NativeScopeID {
					t.Fatalf("%s isolated catalog: %+v %v", id, page, err)
				}
				descriptor := server.adapters[id].descriptor(context.Background())
				if descriptor.ID != id || descriptor.State != "connected" {
					t.Fatalf("%s descriptor: %+v", id, descriptor)
				}
			}
		})
	}
}
