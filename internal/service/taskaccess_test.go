package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func taskAccessFixture(t *testing.T) (*taskSessionsFixture, protocol.SessionAccess) {
	t.Helper()
	fixture := newTaskSessionsFixture(t)
	if _, _, err := fixture.sessions.Create(context.Background(), "first", "client", protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	access, err := fixture.sessions.Access(context.Background(), taskSessionsTaskID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, access
}

func TestTaskAccessSameExecutorAllowsSteerSettingsAndExactStop(t *testing.T) {
	fixture, access := taskAccessFixture(t)
	if !access.Capabilities.Live || access.Capabilities.ActiveSend != "native-steer" || !access.Capabilities.Stop {
		t.Fatalf("active capabilities = %+v", access.Capabilities)
	}
	ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
	if _, err := fixture.sessions.Send(ctx, taskSessionsTaskID, "adjust", "second"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.sessions.UpdateSettings(ctx, taskSessionsTaskID,
		protocol.SessionSettings{Model: protocol.Known("m"), Effort: protocol.Known("high")}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.sessions.ValidateAccess(context.Background(), taskSessionsTaskID, access.Binding); err != nil {
		t.Fatal("settings rotated owner/turn binding", err)
	}
	fixture.sessions.mu.Lock()
	entry := fixture.sessions.entries[taskSessionsTaskID]
	fixture.sessions.mu.Unlock()
	entry.sessions.Receive("thread/tokenUsage/updated", map[string]any{
		"threadId": taskSessionsTaskID, "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": float64(123)}}})
	if err := fixture.sessions.ValidateAccess(context.Background(), taskSessionsTaskID, access.Binding); err != nil {
		t.Fatal("usage rotated owner/turn binding", err)
	}
	if err := fixture.sessions.Stop(ctx, taskSessionsTaskID, "wrong-turn"); err == nil {
		t.Fatal("stop accepted a different native turn")
	}
	if fixture.peer.callCount("turn/interrupt") != 0 {
		t.Fatal("wrong turn reached native interrupt")
	}
	if err := fixture.sessions.Stop(ctx, taskSessionsTaskID, taskSessionsTurnID); err != nil {
		t.Fatal(err)
	}
	if fixture.peer.callCount("turn/steer") != 1 || fixture.peer.callCount("thread/settings/update") != 1 || fixture.peer.callCount("turn/interrupt") != 1 {
		t.Fatal("mutation was missing or replayed", fixture.peer.calls())
	}
	if opens, _ := fixture.factory.counts(); opens != 1 {
		t.Fatal("bound mutation allocated another executor", opens)
	}
}

func TestTaskAccessRejectsChangedExecutionSocketWithoutFollowingOrReplaying(t *testing.T) {
	fixture, access := taskAccessFixture(t)
	otherRPC, otherPeer := newTaskSessionsPeer()
	defer otherRPC.Close()
	fixture.sessions.mu.Lock()
	entry := fixture.sessions.entries[taskSessionsTaskID]
	originalConnection := entry.connection
	entry.connection = &taskSessionsTestConnection{rpc: otherRPC}
	fixture.sessions.mu.Unlock()
	defer func() {
		fixture.sessions.mu.Lock()
		entry.connection = originalConnection
		fixture.sessions.mu.Unlock()
	}()
	ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
	if _, err := fixture.sessions.Send(ctx, taskSessionsTaskID, "stale", "late"); err == nil {
		t.Fatal("changed execution socket accepted old access")
	}
	if err := fixture.sessions.Stop(ctx, taskSessionsTaskID, taskSessionsTurnID); err == nil {
		t.Fatal("changed execution socket accepted old stop")
	}
	if otherPeer.totalCalls() != 0 || fixture.peer.callCount("turn/steer") != 0 || fixture.peer.callCount("turn/interrupt") != 0 {
		t.Fatal("old access dispatched onto an execution socket")
	}
	if opens, follows := fixture.factory.counts(); opens != 1 || follows != 0 {
		t.Fatal("old access reconnected or recovered", opens, follows)
	}
}

func TestTaskAccessIsReadOnlyAndDormantBindingCannotRecover(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	access, err := fixture.sessions.Access(ctx, taskSessionsTaskID)
	if err != nil || access.Binding == "" || access.Capabilities.IdleSend || access.Capabilities.Stop || access.Capabilities.Live {
		t.Fatal(access, err)
	}
	if opens, follows := fixture.factory.counts(); opens != 0 || follows != 0 || fixture.peer.totalCalls() != 0 {
		t.Fatal("access acquired a writer or follower", opens, follows, fixture.peer.calls())
	}
	bound := protocol.WithAccessBinding(ctx, access.Binding)
	if _, err := fixture.sessions.Send(bound, taskSessionsTaskID, "send", "client"); err == nil {
		t.Fatal("dormant binding recovered a writer")
	}
	if opens, follows := fixture.factory.counts(); opens != 0 || follows != 0 || fixture.peer.totalCalls() != 0 {
		t.Fatal("stale mutation reached acquisition", opens, follows, fixture.peer.calls())
	}
}

func TestTaskAccessReplacementAndTurnReuseRejectOldBindingBeforeMutation(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-turn-id-new-generation", true: "replacement-entry"}[replacement], func(t *testing.T) {
			fixture, access := taskAccessFixture(t)
			fixture.sessions.mu.Lock()
			entry := fixture.sessions.entries[taskSessionsTaskID]
			if replacement {
				copyEntry := &taskSessionsEntry{connection: entry.connection, sessions: entry.sessions,
					activity: entry.activity, native: entry.native, accessID: "replacement", executionOwner: entry.executionOwner}
				fixture.sessions.entries[taskSessionsTaskID] = copyEntry
			} else {
				entry.accessEpoch++
			}
			fixture.sessions.mu.Unlock()
			ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
			before := fixture.peer.callCount("turn/steer")
			if _, err := fixture.sessions.Send(ctx, taskSessionsTaskID, "old", "late"); err == nil {
				t.Fatal("stale send succeeded")
			}
			if err := fixture.sessions.Stop(ctx, taskSessionsTaskID, taskSessionsTurnID); err == nil {
				t.Fatal("stale stop succeeded")
			}
			if fixture.peer.callCount("turn/steer") != before || fixture.peer.callCount("turn/interrupt") != 0 {
				t.Fatal("stale binding reached native mutation", fixture.peer.calls())
			}
		})
	}
}

func TestTaskAccessFinalDispatchRechecksAfterSettingsCatalogRead(t *testing.T) {
	fixture, access := taskAccessFixture(t)
	fixture.sessions.mu.Lock()
	entry := fixture.sessions.entries[taskSessionsTaskID]
	fixture.sessions.mu.Unlock()
	release, err := fixture.sessions.bindTaskMutation(protocol.WithAccessBinding(context.Background(), access.Binding), taskSessionsTaskID, entry)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// A mutation's validation may already have succeeded when a replacement
	// arrives. The final request wrapper must still refuse its actual write.
	fixture.sessions.mu.Lock()
	entry.accessEpoch++
	fixture.sessions.mu.Unlock()
	if _, err := entry.native.Request("thread/settings/update", map[string]any{"threadId": taskSessionsTaskID, "model": "m"}); err == nil {
		t.Fatal("dispatch accepted a stale admission")
	}
	if fixture.peer.callCount("thread/settings/update") != 0 {
		t.Fatal("stale settings POST reached native peer")
	}
}

func TestTaskAccessClientForwardsBodyBindingAndRejectsReplacementBeforePost(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "created", true: "recovered"}[recovered], func(t *testing.T) {
			fixture := newTaskClientFixture(t)
			id := fixture.createTask()
			if recovered {
				directory := filepath.Join(fixture.private, "tasks", id)
				if err := os.Rename(filepath.Join(directory, executorOriginName), filepath.Join(directory, executorRecoveryName)); err != nil {
					t.Fatal(err)
				}
			}
			binding := "task1_fixture"
			posts := 0
			fixture.answer = func(path string, input any) (json.RawMessage, error) {
				if strings.HasSuffix(path, "/access") {
					return json.Marshal(protocol.SessionAccess{Binding: binding, Capabilities: taskReadCapabilities()})
				}
				posts++
				body, ok := input.(map[string]any)
				if !ok || body["accessBinding"] != binding || body["nativeScopeId"] != "fixture-native-scope" {
					t.Fatalf("binding absent from private request body: %#v", input)
				}
				switch {
				case strings.HasSuffix(path, "/messages"):
					return json.RawMessage(`{"turnId":"turn","clientId":"client"}`), nil
				case strings.HasSuffix(path, "/settings"):
					return json.RawMessage(`{"updated":true}`), nil
				case strings.HasSuffix(path, "/stop"):
					return json.RawMessage(`{"stopped":true}`), nil
				}
				return nil, errors.New("unexpected request")
			}
			ctx := protocol.WithNativeScopeID(protocol.WithAccessBinding(context.Background(), binding), "fixture-native-scope")
			if _, err := fixture.client.Send(ctx, id, "text", "client"); err != nil {
				t.Fatal(err)
			}
			if err := fixture.client.UpdateSettings(ctx, id, protocol.SessionSettings{Model: protocol.Known("m")}); err != nil {
				t.Fatal(err)
			}
			if err := fixture.client.Stop(ctx, id, "turn"); err != nil {
				t.Fatal(err)
			}
			if posts != 3 {
				t.Fatal("missing or replayed requests", posts)
			}
			binding = "task1_replacement"
			if _, err := fixture.client.Send(ctx, id, "late", "client"); err == nil {
				t.Fatal("replacement accepted old send")
			}
			if err := fixture.client.Stop(ctx, id, "turn"); err == nil {
				t.Fatal("replacement accepted old stop")
			}
			if posts != 3 {
				t.Fatal("replacement received a POST", posts)
			}
		})
	}
}
