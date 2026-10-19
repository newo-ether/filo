package service

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/newo-ether/filo/internal/sessions"
)

const executorRecoveryName = "recovered"

// Created distinguishes native creation from a followed recovery reference.
func (f *TaskExecutorFactory) Created(id string) (bool, error) {
	return taskScope(f.directory, id, false)
}

func (scope createdScope) Created(ctx context.Context, id string) (bool, error) {
	if err := scope.tasks.live(); err != nil {
		return false, err
	}
	return taskScope(scope.tasks.directory, id, false)
}

// admitMutation permits following a recovered executor without granting idle
// recovery. Only exact-task idle admission may allocate a replacement writer.
func (c *CreatedTaskClient) admitMutation(ctx context.Context, id string) error {
	if err := c.live(); err != nil {
		return err
	}
	if taskIDPattern.MatchString(id) && sessions.IdleRecoveryAllowed(ctx, id) {
		return ctx.Err()
	}
	owned, err := taskScope(c.directory, id, true)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("Task is outside Filo creation scope")
	}
	return nil
}

// writeIdleRecoveryHeader carries only a matching explicit mutation to the
// authenticated helper. Context from reads or another task grants nothing.
func writeIdleRecoveryHeader(request *http.Request, ctx context.Context) {
	if request.Method != http.MethodPost {
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/v1/sessions/"), "/")
	if len(parts) != 2 || !taskIDPattern.MatchString(parts[0]) || !sessions.IdleRecoveryAllowed(ctx, parts[0]) {
		return
	}
	switch parts[1] {
	case "messages", "settings", "model":
		request.Header.Set("X-Filo-Idle-Recovery", parts[0])
	}
}

// taskScope separates native creation provenance from explicit idle recovery.
// A recovery reference permits following that exact executor, never a new writer.
func taskScope(directory, id string, recovered bool) (bool, error) {
	if !taskIDPattern.MatchString(id) {
		return false, nil
	}
	names := []string{executorOriginName}
	if recovered {
		names = append(names, executorRecoveryName)
	}
	for _, name := range names {
		value, err := ReadExecutorFile(filepath.Join(directory, "tasks", id), name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if _, err := ExecutorReference(value); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
