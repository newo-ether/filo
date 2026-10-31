package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeTitleMetadataIdentityRenameAndFailureIsolation(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	root := filepath.Join(local, "Claude-3p", "claude-code-sessions", "profile", "account")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "local_fixture.json")
	write := func(id, title string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(`{"cliSessionId":"`+id+`","title":"`+title+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := fixtureAdapter(t)
	registerFixture(t, a, fixtureSession)
	a.mu.Lock()
	a.registrations[fixtureSession].origin = "desktop"
	a.mu.Unlock()
	write(fixtureSession, "Generated native title")
	page, err := a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || page.Runtime.Title == nil || *page.Runtime.Title != "Generated native title" {
		t.Fatal(page.Runtime, err)
	}
	write(fixtureSession, "Renamed while idle")
	listed, err := a.List(context.Background(), "")
	if err != nil || listed.Sessions[0].Title != "Renamed while idle" {
		t.Fatal(listed, err)
	}
	write("20000000-0000-4000-8000-000000000002", "Must not cross identity")
	page, err = a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || *page.Runtime.Title != "Renamed while idle" {
		t.Fatal("metadata failure broke/changed conversation", err)
	}
}

func TestNativeTranscriptGeneratedTitleAndExplicitRename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fixtureSession+".jsonl")
	write := func(rows string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(rows), 0600); err != nil {
			t.Fatal(err)
		}
	}
	generated := `{"type":"ai-title","sessionId":"` + fixtureSession + `","aiTitle":"Native automatic title"}` + "\n"
	write(generated)
	a := fixtureAdapter(t)
	registerFixture(t, a, fixtureSession)
	a.mu.Lock()
	a.registrations[fixtureSession].transcript = path
	a.mu.Unlock()
	page, err := a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || page.Runtime.Title == nil || *page.Runtime.Title != "Native automatic title" {
		t.Fatal("generated title missing from conversation", page.Runtime, err)
	}
	custom := `{"type":"custom-title","sessionId":"` + fixtureSession + `","customTitle":"Personal rename"}` + "\n"
	wrong := `{"type":"ai-title","sessionId":"20000000-0000-4000-8000-000000000002","aiTitle":"Wrong session"}` + "\n"
	for _, rows := range []string{generated + custom, custom + generated + wrong} {
		write(rows)
		listed, err := a.List(context.Background(), "")
		if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].Title != "Personal rename" {
			t.Fatal("explicit native title must win over generated title", listed, err)
		}
	}
	write(wrong)
	if title := transcriptTitle(path, fixtureSession); title != nil {
		t.Fatal("generated title crossed session identity", title)
	}
	write(`{"type":"ai-title","sessionId":"` + fixtureSession + `","aiTitle":"` + strings.Repeat("x", 4097) + `"}` + "\n")
	if title := transcriptTitle(path, fixtureSession); title != nil {
		t.Fatal("oversize generated title admitted", title)
	}
}

func TestNativeTranscriptTitleReadsOnlyScopedBoundedRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, fixtureSession+".jsonl")
	data := `{"type":"custom-title","sessionId":"` + fixtureSession + `","customTitle":"Generated"}` + "\n" + `{"type":"custom-title","sessionId":"` + fixtureSession + `","customTitle":"Renamed"}` + "\n"
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	title := transcriptTitle(file, fixtureSession)
	if title == nil || *title != "Renamed" {
		t.Fatal(title)
	}
	if transcriptTitle(file, "20000000-0000-4000-8000-000000000002") != nil {
		t.Fatal("wrong transcript identity")
	}
	outside := filepath.Join(t.TempDir(), fixtureSession+".jsonl")
	if err := os.WriteFile(outside, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if transcriptTitle(outside, fixtureSession) != nil {
		t.Fatal("metadata escaped native project root")
	}
	initial := `{"type":"custom-title","sessionId":"` + fixtureSession + `","customTitle":"Initial large transcript title"}` + "\n"
	large := initial + strings.Repeat(`{"type":"progress"}`+"\n", 10000)
	if err := os.WriteFile(file, []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	if title := transcriptTitle(file, fixtureSession); title == nil || *title != "Initial large transcript title" {
		t.Fatal("initial title lost beyond tail window", title)
	}
	large += `{"type":"custom-title","sessionId":"` + fixtureSession + `","customTitle":"Latest rename"}` + "\n"
	if err := os.WriteFile(file, []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	if title := transcriptTitle(file, fixtureSession); title == nil || *title != "Latest rename" {
		t.Fatal("latest rename did not override initial title", title)
	}
}
