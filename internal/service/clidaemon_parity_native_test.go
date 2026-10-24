//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// This opt-in probe creates one tool-free native daemon thread. Ordinary TUI
// controls have a separate qualification; no existing thread is mutated here.
func TestCLIDaemonNativeTitleAndLiveProjection(t *testing.T) {
	if os.Getenv("FILO_CLI_PARITY_PROBE") != "1" {
		t.Skip("explicit self-created native daemon parity probe is disabled")
	}
	home, connect := parityDaemonConnector(t)
	homeReader := func(context.Context) (string, error) { return home, nil }
	host, err := connect(context.Background())
	if err != nil {
		t.Fatalf("native daemon connector: %v", err)
	}
	defer host.Close()
	root := t.TempDir()
	created, err := host.Rpc.Request("thread/start", map[string]any{
		"cwd": root, "approvalPolicy": "never", "sandbox": "read-only",
		"developerInstructions": "Follow the user's text request. Do not call any tools or modify files.",
	})
	if err != nil {
		t.Fatalf("native fixture creation failed; not replayed: %v", err)
	}
	thread := sharedProbeObject(sharedProbeObject(created)["thread"])
	id, _ := thread["id"].(string)
	path, _ := thread["cwd"].(string)
	if !uuidPattern.MatchString(id) || !paritySameDirectory(path, root) {
		t.Fatal("self-created native identity/cwd admission failed")
	}
	var turnID string
	defer func() {
		cleanupHost, err := connect(context.Background())
		if err != nil {
			t.Errorf("native cleanup connector: %v", err)
			return
		}
		defer cleanupHost.Close()
		cleanupParityThread(t, cleanupHost, id, root, &turnID)
	}()
	started, err := host.Rpc.Request("turn/start", map[string]any{"threadId": id,
		"input": []any{map[string]any{"type": "text", "text": "Write 90 numbered complete sentences about sorting colored wooden blocks. Each sentence must contain at least 18 words. Include every line without abbreviating. Do not call tools."}},
	})
	if err != nil {
		t.Fatalf("native initial input failed; not replayed: %v", err)
	}
	turnID, _ = sharedProbeObject(sharedProbeObject(started)["turn"])["id"].(string)
	if !uuidPattern.MatchString(turnID) {
		t.Fatal("native initial turn identity missing")
	}
	nativeHistory := history.NewNativeHistory(homeReader)
	scope := func(context.Context) (string, error) { return "self-created-daemon-parity", nil }
	metadata := newCLISourceMetadata(scope, func(_ context.Context, selected string) error {
		if !strings.EqualFold(filepath.Clean(selected), filepath.Clean(home)) {
			return codexcli.ErrUnavailable
		}
		return nil
	})
	defer metadata.Close()
	reader, err := codexcli.New(codexcli.Options{History: nativeHistory, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	surface, err := NewCLIControlledSessions(CLIControlledOptions{Directory: t.TempDir(), Workspace: root,
		Reader: reader, Metadata: metadata, History: nativeHistory, Scope: scope,
		NativeHome: homeReader, Daemon: connect})
	if err != nil {
		t.Fatal(err)
	}
	defer surface.Close()
	if _, err := surface.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	detach, err := surface.Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	lengths := map[string]map[int]bool{}
	active, initialTitle, renamed, renameMatches, idle := false, false, false, false, false
	name := "Filo native title parity " + id[:8]
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		page, err := surface.Read(context.Background(), id, "", true, nil)
		if err != nil || page.Runtime == nil {
			t.Fatalf("production native projection: %v", err)
		}
		runtime := page.Runtime
		running := runtime.Status == "active" && runtime.ActiveTurnID != nil && *runtime.ActiveTurnID == turnID
		active = active || running && runtime.ActiveTurnHasUserMessage.Value
		if running {
			for _, row := range page.Messages {
				if row.Role == "assistant" && row.Activity == nil && row.Text != "" {
					if lengths[row.ID] == nil {
						lengths[row.ID] = map[int]bool{}
					}
					lengths[row.ID][len(row.Text)] = true
				}
			}
		}
		catalog, err := surface.List(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		var title protocol.Text
		for _, row := range catalog.Sessions {
			if row.ID == id {
				title = row.Title
			}
		}
		if !renamed && runtime.Title != nil && *runtime.Title != "" && *runtime.Title == title {
			initialTitle = true
			if _, err := host.Rpc.Request("thread/name/set", map[string]any{"threadId": id, "name": name}); err != nil {
				t.Fatalf("native fixture rename refused; not replayed: %v", err)
			}
			renamed = true
		}
		renameMatches = renameMatches || renamed && runtime.Title != nil && string(*runtime.Title) == name && string(title) == name
		idle = runtime.Status == "idle" && runtime.ActiveTurnID == nil && runtime.CompletedTurnID.Known &&
			runtime.CompletedTurnID.Value != nil && *runtime.CompletedTurnID.Value == turnID
		if idle && renameMatches {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	incremental := false
	for _, values := range lengths {
		incremental = incremental || len(values) > 1
	}
	if !active || !initialTitle || !renameMatches || !idle || !incremental {
		t.Fatalf("native parity active=%v initial-title=%v rename=%v idle=%v incremental=%v", active, initialTitle, renameMatches, idle, incremental)
	}
	t.Logf("same native daemon thread=%s active-user, incremental text, initial title, rename/list and exact idle passed", id)
}

func parityDaemonConnector(t *testing.T) (string, func(context.Context) (*codex.CodexHost, error)) {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(account.HomeDir, ".codex")
	homeReader := func(context.Context) (string, error) { return home, nil }
	verify := func(_ context.Context, identity codex.LocalDaemonIdentity) error {
		if !strings.EqualFold(filepath.Clean(identity.CodexHome), filepath.Clean(home)) {
			return codexcli.ErrUnavailable
		}
		return nil
	}
	return home, existingCLIDaemon("", account.Uid, homeReader, verify)
}

// Recovery archives only a completed, explicitly selected fixture from this probe.
// It never starts a turn, interrupts a writer, or admits another native session.
func TestCLIDaemonParityArchiveCompletedFixture(t *testing.T) {
	id, cwd := os.Getenv("FILO_CLI_PARITY_CLEANUP_THREAD"), os.Getenv("FILO_CLI_PARITY_CLEANUP_CWD")
	if id == "" && cwd == "" {
		t.Skip("explicit completed fixture cleanup is disabled")
	}
	if !uuidPattern.MatchString(id) || !filepath.IsAbs(cwd) || filepath.Base(cwd) != "001" ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(cwd)), "TestCLIDaemonNativeTitleAndLiveProjection") {
		t.Fatal("cleanup target is not an explicit parity fixture")
	}
	_, connect := parityDaemonConnector(t)
	host, err := connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	value, err := host.Rpc.Request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
	thread := sharedProbeObject(sharedProbeObject(value)["thread"])
	if err != nil || thread["id"] != id || thread["name"] != "Filo native title parity "+id[:8] {
		t.Fatalf("completed fixture identity/title unavailable: %v", err)
	}
	t.Logf("cleanup exact thread=%s native-cwd=%v expected-cwd=%s", id, thread["cwd"], cwd)
	value, err = host.Rpc.Request("thread/turns/list", map[string]any{
		"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "summary",
	})
	rows, _ := sharedProbeObject(value)["data"].([]any)
	if err != nil || len(rows) != 1 {
		t.Fatalf("completed fixture latest turn unavailable: %v", err)
	}
	turn := sharedProbeObject(rows[0])
	turnID, _ := turn["id"].(string)
	if !uuidPattern.MatchString(turnID) || turn["status"] != "completed" {
		t.Fatal("completed fixture terminal turn unconfirmed")
	}
	t.Logf("cleanup exact completed turn=%s", turnID)
	cleanupParityThread(t, host, id, cwd, &turnID)
}

func cleanupParityThread(t *testing.T, host *codex.CodexHost, id, cwd string, turnID *string) {
	t.Helper()
	value, err := host.Rpc.Request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
	thread := sharedProbeObject(sharedProbeObject(value)["thread"])
	path, _ := thread["cwd"].(string)
	if err != nil || thread["id"] != id || !paritySameDirectory(path, cwd) {
		t.Errorf("exact native cleanup identity/cwd unavailable: error=%v id=%v cwd=%q expected=%q", err, thread["id"], path, cwd)
		return
	}
	latest := func() (string, string, error) {
		value, err := host.Rpc.Request("thread/turns/list", map[string]any{
			"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "summary",
		})
		rows, _ := sharedProbeObject(value)["data"].([]any)
		if err != nil || len(rows) != 1 {
			return "", "", fmt.Errorf("exact latest turn unavailable")
		}
		row := sharedProbeObject(rows[0])
		return fmt.Sprint(row["id"]), fmt.Sprint(row["status"]), nil
	}
	current, state, err := latest()
	if err != nil || current != *turnID {
		t.Error("exact native cleanup turn unavailable")
		return
	}
	if state == "inProgress" {
		if _, err := host.Rpc.Request("turn/interrupt", map[string]any{"threadId": id, "turnId": *turnID}); err != nil {
			t.Error("exact native cleanup interrupt failed; not replayed")
			return
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			current, state, err = latest()
			if err != nil || current != *turnID || state != "inProgress" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err != nil || current != *turnID || state == "inProgress" {
		t.Error("exact native cleanup terminal unconfirmed")
		return
	}
	if _, err := host.Rpc.Request("thread/archive", map[string]any{"threadId": id}); err != nil {
		t.Error("exact native archive failed; not replayed")
		return
	}
	t.Logf("native archive cleanup confirmed thread=%s", id)
}

func paritySameDirectory(left, right string) bool {
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && leftInfo.IsDir() && rightInfo.IsDir() && os.SameFile(leftInfo, rightInfo)
}
