//go:build windows

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAssemblyClaudeOnlyPrivateWorkerNeverConnectsDesktopHost(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	if !systemServiceSIDPattern.MatchString(sid) {
		t.Skip("fixture requires an ordinary Windows account")
	}
	native := newStandaloneNativeFixture(t, false)
	directory := t.TempDir()
	port, bridgePort := reserveLoopbackPort(t), reserveLoopbackPort(t)
	for bridgePort == port {
		bridgePort = reserveLoopbackPort(t)
	}
	config := map[string]any{"sources": []string{claudeCodeSource}, "desktopSid": sid,
		"bind": "127.0.0.1", "port": port, "claudeBridgePort": bridgePort, "appServerUrl": native.url()}
	writeEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(Marshal(config)))
	writeEntryFixtureFile(t, filepath.Join(directory, entryTokenName), testToken)
	done := make(chan int, 1)
	go func() {
		done <- runStandalone(standaloneOptions{Arguments: []string{directory}, Output: io.Discard, Error: io.Discard})
	}()
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	info := awaitEntryInfo(t, base, entryBearerHeaders(testToken), func(info map[string]any) bool {
		return info["extensions"] != nil
	})
	adapters, _ := info["adapters"].([]any)
	if len(adapters) != 1 {
		t.Fatalf("private sources: %v", info)
	}
	if native.connectionCount() != 0 {
		t.Fatal("Claude-only worker connected Desktop auxiliary")
	}
	secret, err := readEntryText(filepath.Join(directory, "claude-mod-token"))
	if err != nil || secret == testToken {
		t.Fatal("private bridge secret unavailable or reused")
	}
	request, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(bridgePort)+"/claude-mod/v1/register",
		strings.NewReader(`{"sessionId":"11111111-1111-4111-8111-111111111111","instance":"fixture","cwd":"C:/fixture","origin":"terminal","version":"fixture"}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var registration map[string]any
	err = json.NewDecoder(response.Body).Decode(&registration)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("registration: %v %v", registration, err)
	}
	stopped := post(t, base+"/v1/shutdown", "", entryBearerHeaders(testToken))
	stopped.Body.Close()
	if code := awaitEntryExit(t, done); code != 0 {
		t.Fatal(code)
	}
	if native.connectionCount() != 0 || !native.reachable() {
		t.Fatal("worker touched independent Desktop host")
	}
	if _, err := os.Stat(filepath.Join(directory, serviceTaskDirectoryName)); !os.IsNotExist(err) {
		t.Fatal("Claude-only task executor allocation")
	}
}
