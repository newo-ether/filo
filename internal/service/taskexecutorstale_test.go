package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/sessions"
)

func TestTaskExecutorFactoryDeadWriterNeedsExactIdleRecovery(t *testing.T) {
	const missingPID = 1 << 30
	if processAlive(missingPID) {
		t.Skip("fixture PID is unexpectedly live")
	}
	for _, phase := range []string{"ready", "disconnected"} {
		t.Run(phase, func(t *testing.T) {
			f := newExecutorFactoryFixture(t)
			factory := factoryFor(t, f, 4, 30*time.Millisecond)
			defer factory.Close()
			id := "11111111-1111-4111-8111-111111111111"
			ctx := sessions.WithIdleRecovery(context.Background(), id)
			conn, err := factory.Open(ctx, &id)
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			entry := f.runningExecutor(0)
			f.mutate(entry, func(state *ExecutorState) {
				state.Phase = phase
				state.NativePid = new(int(missingPID))
				state.GuardianPid = new(int(missingPID))
				state.URL = factoryTextPointer("ws://127.0.0.1:1")
			})
			if err := SaveExecutorState(entry.directory, f.snapshot(entry)); err != nil {
				t.Fatal(err)
			}
			if followed, err := factory.Follow(ctx, id); err != nil || followed != nil || f.runningCount() != 1 {
				t.Fatal("browse allocated or failed instead of history fallback", followed, err)
			}
			for _, denied := range []context.Context{context.Background(), sessions.WithIdleRecovery(context.Background(), "22222222-2222-4222-8222-222222222222")} {
				if _, err := factory.Open(denied, &id); err == nil || f.runningCount() != 1 {
					t.Fatal("unadmitted dead writer allocated", err)
				}
			}
			conn, err = factory.Open(ctx, &id)
			if err != nil || f.runningCount() != 2 {
				t.Fatal("proven dead writer blocked explicit idle recovery", err)
			}
			conn.Close()
			if inputs := f.inputList(); len(inputs) != 0 {
				t.Fatal("reconnection replayed native input", inputs)
			}
		})
	}
}

func TestExecutorWriterEndedRequiresPublishedWriterAndNoGuardian(t *testing.T) {
	const missingPID = 1 << 30
	if processAlive(missingPID) {
		t.Skip("fixture PID is unexpectedly live")
	}
	livePID := os.Getpid()
	for _, test := range []struct {
		name  string
		state ExecutorState
		ended bool
	}{
		{"ready dead", ExecutorState{Phase: "ready", KeeperPid: livePID, NativePid: new(int(missingPID))}, true},
		{"disconnected dead", ExecutorState{Phase: "disconnected", KeeperPid: livePID, NativePid: new(int(missingPID)), GuardianPid: new(int(missingPID))}, true},
		{"starting", ExecutorState{Phase: "starting", KeeperPid: livePID, NativePid: new(int(missingPID))}, false},
		{"no writer identity", ExecutorState{Phase: "disconnected", KeeperPid: livePID}, false},
		{"live writer", ExecutorState{Phase: "ready", KeeperPid: livePID, NativePid: &livePID}, false},
		{"live guardian", ExecutorState{Phase: "disconnected", KeeperPid: livePID, NativePid: new(int(missingPID)), GuardianPid: &livePID}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := executorWriterEnded(test.state); got != test.ended {
				t.Fatalf("ended = %v, want %v", got, test.ended)
			}
		})
	}
}
