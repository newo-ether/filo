package service

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const executorTaskID = "11111111-1111-4111-8111-111111111111"

func cloneValues(value map[string]any) map[string]any {
	copied := make(map[string]any, len(value))
	for key, raw := range value {
		copied[key] = raw
	}
	return copied
}

func executorConfigFixture(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"token":       strings.Repeat("a", 64),
		"nativeToken": strings.Repeat("b", 64),
		"workspace":   t.TempDir(),
		"taskId":      executorTaskID,
	}
}

func TestExecutorConfigurationKeepsSeparateCredentialsAndExactTaskAdmission(t *testing.T) {
	base := executorConfigFixture(t)
	base["createTrace"] = strings.Repeat("c", 32)
	config, err := ParseExecutorConfig(base)
	if err != nil {
		t.Fatalf("config = %v", err)
	}
	if config.TaskID == nil || *config.TaskID != executorTaskID ||
		config.CreateTrace != strings.Repeat("c", 32) ||
		config.Token != strings.Repeat("a", 64) || config.Workspace != base["workspace"] {
		t.Fatalf("config = %+v", config)
	}

	changes := []map[string]any{
		{"token": "broken"},
		{"nativeToken": strings.Repeat("a", 64)},
		{"nativeToken": ""},
		{"workspace": "relative"},
		{"taskId": []any{executorTaskID}},
		{"taskId": "../ordinary-task"},
		{"taskId": nil},
		{"createTrace": strings.Repeat("C", 32)},
		{"createTrace": strings.Repeat("c", 31)},
		{"createTrace": nil},
	}
	for _, change := range changes {
		value := cloneValues(base)
		for key, raw := range change {
			value[key] = raw
		}
		latest, err := ParseExecutorConfig(value)
		if err == nil || !strings.Contains(err.Error(), "Invalid executor configuration") {
			t.Fatalf("%v = %+v, %v", change, latest, err)
		}
	}

	// An executor that has not accepted a task yet carries no task identity.
	unassigned := cloneValues(base)
	delete(unassigned, "taskId")
	delete(unassigned, "createTrace")
	if config, err := ParseExecutorConfig(unassigned); err != nil || config.TaskID != nil || config.CreateTrace != "" {
		t.Fatalf("unassigned = %+v, %v", config, err)
	}
}

func TestReadyExecutorDiscoveryRequiresCompleteLoopbackEndpointAndProcessIdentity(t *testing.T) {
	ready := map[string]any{
		"phase":     "ready",
		"keeperPid": float64(123),
		"nativePid": float64(456),
		"nativeUrl": "ws://127.0.0.1:4567",
		"url":       "ws://127.0.0.1:4568",
		"taskId":    executorTaskID,
	}
	state, err := ParseExecutorState(ready)
	if err != nil {
		t.Fatalf("state = %v", err)
	}
	if state.Phase != "ready" || state.KeeperPid != 123 || state.NativePid == nil || *state.NativePid != 456 ||
		state.NativeURL == nil || *state.NativeURL != "ws://127.0.0.1:4567" || state.URL == nil ||
		state.TaskID == nil || *state.TaskID != executorTaskID {
		t.Fatalf("state = %+v", state)
	}

	changes := []func(map[string]any){
		func(value map[string]any) { value["keeperPid"] = float64(-1) },
		func(value map[string]any) { value["keeperPid"] = float64(1.5) },
		func(value map[string]any) { value["keeperPid"] = float64(9007199254740992) },
		func(value map[string]any) { value["keeperPid"] = "123" },
		func(value map[string]any) { delete(value, "nativePid") },
		func(value map[string]any) { delete(value, "url") },
		func(value map[string]any) { value["url"] = "ws://example.com:4568" },
		func(value map[string]any) { value["url"] = "ws://127.0.0.1:4568/?token=secret" },
		func(value map[string]any) { value["nativeUrl"] = "ws://127.0.0.1:0" },
		func(value map[string]any) { value["nativeUrl"] = nil },
		func(value map[string]any) { value["phase"] = "unknown" },
		func(value map[string]any) { delete(value, "phase") },
		func(value map[string]any) { value["taskId"] = []any{executorTaskID} },
		func(value map[string]any) { value["guardianPid"] = float64(0) },
	}
	for index, change := range changes {
		value := cloneValues(ready)
		change(value)
		if accepted, err := ParseExecutorState(value); err == nil {
			t.Fatalf("change %d accepted %+v", index, accepted)
		}
	}

	// A starting or exited record needs no endpoint, only the keeper identity.
	for _, phase := range []string{"starting", "disconnected", "retired", "exited"} {
		value := map[string]any{"phase": phase, "keeperPid": float64(123)}
		state, err := ParseExecutorState(value)
		if err != nil || state.URL != nil || state.NativePid != nil {
			t.Fatalf("%s = %+v, %v", phase, state, err)
		}
	}
	if _, err := ParseExecutorState([]any{}); err == nil {
		t.Fatal("an array was accepted as executor state")
	}
	if _, err := ParseExecutorState("ready"); err == nil {
		t.Fatal("a string was accepted as executor state")
	}
}

func TestDurableExecutorStateReplacesAtomicallyAndRejectsOversizedOrMalformedInput(t *testing.T) {
	directory := t.TempDir()
	before := ExecutorState{Phase: "starting", KeeperPid: 123}
	if err := SaveExecutorState(directory, before); err != nil {
		t.Fatalf("save = %v", err)
	}
	decoded, err := ReadExecutorFile(directory, executorStateName)
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	if !reflect.DeepEqual(decoded, map[string]any{"phase": "starting", "keeperPid": float64(123)}) {
		t.Fatalf("state = %v", decoded)
	}

	after := ExecutorState{Phase: "exited", KeeperPid: 123, TaskID: pointerTo(executorTaskID)}
	if err := SaveExecutorState(directory, after); err != nil {
		t.Fatalf("save = %v", err)
	}
	decoded, err = ReadExecutorFile(directory, executorStateName)
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	state, err := ParseExecutorState(decoded)
	if err != nil || state.Phase != "exited" || state.TaskID == nil || *state.TaskID != executorTaskID {
		t.Fatalf("state = %+v, %v", state, err)
	}

	if err := SaveExecutorState(directory, ExecutorState{Phase: "exited", KeeperPid: 0}); err == nil {
		t.Fatal("an invalid state was saved")
	}
	published, err := os.ReadFile(filepath.Join(directory, executorStateName))
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	if string(published) != string(Marshal(after)) {
		t.Fatalf("published = %s, want %s", published, Marshal(after))
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("readdir = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != executorStateName {
		t.Fatalf("directory = %v", entries)
	}

	oversized := filepath.Join(directory, "bad.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte(" "), maxExecutorFileBytes+1), 0o600); err != nil {
		t.Fatalf("write = %v", err)
	}
	if _, err := ReadExecutorFile(directory, "bad.json"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized = %v", err)
	}
	if err := os.WriteFile(oversized, []byte("{"), 0o600); err != nil {
		t.Fatalf("write = %v", err)
	}
	if _, err := ReadExecutorFile(directory, "bad.json"); err == nil {
		t.Fatal("malformed state was accepted")
	}
}

func TestCreatedTaskProvenanceComesFromTheExecutorDirectoryLayout(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "executors", executorTaskID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("mkdir = %v", err)
	}
	taskID := "22222222-2222-4222-8222-222222222222"
	if err := RecordCreatedTask(directory, taskID); err != nil {
		t.Fatalf("record = %v", err)
	}
	origin, err := os.ReadFile(filepath.Join(root, "tasks", taskID, "origin.json"))
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	if string(origin) != `{"executorId":"`+executorTaskID+`"}` {
		t.Fatalf("origin = %s", origin)
	}

	// Creation provenance is written once per task and only from the executor
	// directory the layout declares.
	if err := RecordCreatedTask(directory, taskID); err == nil {
		t.Fatal("the same creation was recorded twice")
	}
	if err := RecordCreatedTask(filepath.Join(root, "elsewhere", executorTaskID), taskID); err == nil {
		t.Fatal("a foreign parent directory was accepted")
	}
	if err := RecordCreatedTask(directory, "../ordinary-task"); err == nil {
		t.Fatal("an escaping task id was accepted")
	}
}

func TestExecutorReferenceReadsTheRecordedIdentity(t *testing.T) {
	id, err := ExecutorReference(map[string]any{"executorId": executorTaskID})
	if err != nil || id != executorTaskID {
		t.Fatalf("reference = %q, %v", id, err)
	}
	rejected := []any{
		map[string]any{"executorId": "../ordinary-task"},
		map[string]any{"executorId": float64(7)},
		map[string]any{},
		"text",
		nil,
		[]any{executorTaskID},
	}
	for _, value := range rejected {
		if id, err := ExecutorReference(value); err == nil {
			t.Fatalf("%v = %q", value, id)
		}
	}
}

func pointerTo(value string) *string { return &value }
