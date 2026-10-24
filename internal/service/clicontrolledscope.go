package service

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// executorSource preserves Desktop provenance written before multiple native
// runtimes were supported. CLI always requires an explicit source marker.
func executorSource(source string) string {
	if source == "" {
		return codexDesktopSource
	}
	return source
}

func (f *TaskExecutorFactory) ownsSource(id string) (bool, error) {
	task := filepath.Join(f.directory, "tasks", id)
	for _, name := range []string{executorOriginName, executorRecoveryName} {
		value, err := ReadExecutorFile(task, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		executorID, err := ExecutorReference(value)
		if err != nil {
			return false, err
		}
		value, err = ReadExecutorFile(filepath.Join(f.directory, "executors", executorID), executorConfigName)
		if errors.Is(err, fs.ErrNotExist) && executorSource(f.source) == codexDesktopSource {
			return true, nil // Legacy Desktop provenance predates source markers.
		}
		if err != nil {
			return false, err
		}
		config, err := ParseExecutorConfig(value)
		if err != nil {
			return false, err
		}
		return executorSource(config.Source) == executorSource(f.source), nil
	}
	return false, nil
}
