//go:build windows

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/newo-ether/filo/internal/codex"
	"golang.org/x/sys/windows"
)

func TestCLISharedOwnerProcess(t *testing.T) {
	if os.Getenv("FILO_CLI_OWNER_CHILD") != "1" {
		t.Skip("isolated owner child")
	}
	if err := RunCLISharedOwner(os.Getenv("FILO_CLI_OWNER_DIRECTORY")); err != nil {
		t.Fatal(err)
	}
}

func TestCLISharedOwnerSurvivesWrapperLossUntilNativeCompletion(t *testing.T) {
	if os.Getenv("FILO_CLI_SHARED_PROBE") != "1" {
		t.Skip("explicit shared native qualification is disabled")
	}
	runtimePath, err := resolveCLIRuntime()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	directory := filepath.Join(root, "registry", "owner")
	if err := prepareCLISharedDirectory(directory); err != nil {
		t.Fatal(err)
	}
	token, err := executorSecret()
	if err != nil {
		t.Fatal(err)
	}
	lease := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lease.Close()
	config := cliSharedOwnerConfig{Runtime: runtimePath, Workspace: root, Token: token,
		Lease: lease.URL + "/lease"}
	if err := SaveExecutorFile(directory, cliSharedOwnerConfigName, config); err != nil {
		t.Fatal(err)
	}
	consoleLog := filepath.Join(root, "owner-console.log")
	owner := exec.Command(os.Args[0], "-test.run=^TestCLISharedOwnerProcess$", "-test.timeout=120s")
	owner.Env = append(os.Environ(), "FILO_CLI_OWNER_CHILD=1", "FILO_CLI_OWNER_DIRECTORY="+directory,
		serviceHelperConsoleLogEnvironment+"="+consoleLog)
	owner.Dir, owner.SysProcAttr = root, detachedProcessAttributes()
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	ownerDone := make(chan struct{})
	var ownerErr error
	go func() { ownerErr = owner.Wait(); close(ownerDone) }()
	defer func() {
		select {
		case <-ownerDone:
		default:
			_ = owner.Process.Kill()
			<-ownerDone
		}
	}()
	assertNoChildConsole(t, consoleLog)
	var record cliSharedRecord
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		record, err = readCLISharedRecord(directory)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("independent owner did not publish readiness")
	}
	// Cleanup uses the exact native child owned by this fixture, never a process
	// name or a user-running session. This also runs on failed assertions.
	native, err := sharedProbeNativeChild(owner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Kill()
	host, err := codex.ConnectCodexHost(context.Background(), record.URL,
		codex.HostOptions{Token: &token, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	events := &sharedProbeEvents{}
	host.Rpc.SetHandlers(nil, events.receive)
	created, err := host.Rpc.Request("thread/start", map[string]any{"cwd": root,
		"approvalPolicy": "never", "sandbox": "read-only",
		"developerInstructions": "Reply exactly OK. Do not call any tools."})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := sharedProbeObject(sharedProbeObject(created)["thread"])["id"].(string)
	started, err := host.Rpc.Request("turn/start", map[string]any{"threadId": id,
		"input": []any{map[string]any{"type": "text", "text": "Reply exactly OK. Do not call tools."}}})
	if err != nil {
		t.Fatal("native input failed; no retry")
	}
	turn, _ := sharedProbeObject(sharedProbeObject(started)["turn"])["id"].(string)
	deadline = time.Now().Add(10 * time.Second)
	for !events.has("turn/started", id, turn) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !events.has("turn/started", id, turn) {
		t.Fatal("missing exact native start")
	}
	lease.Close()
	deadline = time.Now().Add(90 * time.Second)
	for !events.has("turn/completed", id, turn) && time.Now().Before(deadline) {
		select {
		case <-ownerDone:
			t.Fatalf("owner ended before accepted turn completion: %v", ownerErr)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !events.assistantOK(turn) || events.turnStatus(turn) != "completed" {
		t.Fatalf("accepted native turn after wrapper loss: status=%s assistant=%q",
			events.turnStatus(turn), events.assistantText(turn))
	}
	if _, err := os.Stat(filepath.Join(directory, cliSharedRecordName)); !os.IsNotExist(err) {
		t.Fatal("expired wrapper rendezvous was not fenced")
	}
	select {
	case <-ownerDone:
		t.Fatal("owner forcibly retired a published native child")
	default:
	}
	if err := native.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ownerDone:
		if ownerErr != nil {
			t.Fatalf("owner cleanup failed: %v", ownerErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("owner did not observe its native child exit")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("owner registry was not removed")
	}
	t.Log("hidden owner fenced lost wrapper, preserved exact OK turn, and observed its fixture child exit")
}

func sharedProbeNativeChild(parent int) (*os.Process, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if int(entry.ParentProcessID) == parent && windows.UTF16ToString(entry.ExeFile[:]) == "codex.exe" {
			return os.FindProcess(int(entry.ProcessID))
		}
	}
	return nil, errors.New("fixture native child was not found")
}
