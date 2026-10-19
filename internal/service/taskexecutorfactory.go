package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
)

const (
	// DefaultExecutorFactoryCapacity bounds live executors per leased user helper.
	DefaultExecutorFactoryCapacity = 4
	// executorRunnerArgument selects the runner entry in the same binary.
	executorRunnerArgument = "executor-runner"
	// executorReadinessPoll controls the interval between readiness reads.
	executorReadinessPoll = 100 * time.Millisecond
	// executorFollowReadiness bounds a read-only follow, which must never wait
	// the full cold-start budget for a writer that may already be gone.
	executorFollowReadiness = 2 * time.Second
	// executorConfigName is the per-executor private configuration record.
	executorConfigName = "config.json"
	// executorClaimName is the admission claim written before configuration, so
	// an interrupted creation stays visible instead of looking released.
	executorClaimName = "started"
)

// ExecutorLauncher starts the native process of one prepared executor
// directory. The launcher owns its child: closing an executor connection never
// signals a native process.
type ExecutorLauncher func(directory string) error

// TaskExecutorFactoryOptions configures one factory.
//
// Capacity == 0 selects DefaultExecutorFactoryCapacity. Readiness <= 0 selects
// the protocol executor readiness budget. Deployment paths must be absolute.
type TaskExecutorFactoryOptions struct {
	// Directory is the absolute private executor root.
	Directory string
	// Workspace is the absolute native workspace recorded in every configuration.
	Workspace string
	// Launch starts one executor. Nil selects a detached copy of this binary
	// running the runner subcommand.
	Launch ExecutorLauncher
	// Capacity bounds concurrently live native writers.
	Capacity int
	// Readiness bounds how long a connection waits for a published endpoint.
	Readiness time.Duration
}

// executorOpening is one in-flight connection attempt, shared by every caller
// that asked for the same task while it was starting.
type executorOpening struct {
	done chan struct{}
	conn *codex.CodexHost
	err  error
}

// TaskExecutorFactory opens private task executors for one leased user helper.
// It serializes allocation, reuses in-flight connections and never signals a
// native process when a connection closes.
type TaskExecutorFactory struct {
	directory string
	workspace string
	launch    ExecutorLauncher
	capacity  int
	readiness time.Duration

	// admission serializes allocation so concurrent creations cannot both
	// observe the same free capacity.
	admission sync.Mutex

	mu      sync.Mutex
	opening map[string]*executorOpening
	stopped bool
}

// NewTaskExecutorFactory validates the private paths and prepares the factory.
func NewTaskExecutorFactory(options TaskExecutorFactoryOptions) (*TaskExecutorFactory, error) {
	if !absoluteWorkspace(options.Directory) || !absoluteWorkspace(options.Workspace) {
		return nil, errors.New("Executor paths must be absolute")
	}
	factory := &TaskExecutorFactory{
		directory: options.Directory,
		workspace: options.Workspace,
		launch:    options.Launch,
		capacity:  options.Capacity,
		readiness: options.Readiness,
		opening:   make(map[string]*executorOpening),
	}
	if factory.launch == nil {
		factory.launch = launchExecutorRunner
	}
	if factory.capacity <= 0 {
		factory.capacity = DefaultExecutorFactoryCapacity
	}
	if factory.readiness <= 0 {
		factory.readiness = protocol.DefaultTaskRequestTimeouts.ExecutorReady
	}
	return factory, nil
}

// Owns reports creation or recovery provenance. Recovery permits reconnecting
// the recorded executor; replacement writers still require idle admission.
func (f *TaskExecutorFactory) Owns(taskID string) (bool, error) {
	return taskScope(f.directory, taskID, true)
}

// Open returns a connection to the private executor of one task, allocating a
// new native writer only for an admitted task or a brand new one. Concurrent
// calls for the same task share a single connection attempt and a single
// native launch.
func (f *TaskExecutorFactory) Open(ctx context.Context, taskID *string) (*codex.CodexHost, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if f.isStopped() {
		return nil, errors.New("Filo executor factory is closing")
	}
	if taskID != nil && !taskIDPattern.MatchString(*taskID) {
		return nil, errors.New("Invalid task identity")
	}
	key := ""
	if taskID != nil {
		key = *taskID
	} else {
		generated, err := randomUUID()
		if err != nil {
			return nil, err
		}
		key = generated
	}
	f.mu.Lock()
	if entry, ok := f.opening[key]; ok {
		f.mu.Unlock()
		<-entry.done
		return entry.conn, entry.err
	}
	entry := &executorOpening{done: make(chan struct{})}
	f.opening[key] = entry
	f.mu.Unlock()
	conn, err := f.openOnce(ctx, taskID)
	f.mu.Lock()
	entry.conn, entry.err = conn, err
	delete(f.opening, key)
	f.mu.Unlock()
	close(entry.done)
	return conn, err
}

func (f *TaskExecutorFactory) openOnce(ctx context.Context, taskID *string) (*codex.CodexHost, error) {
	if taskID == nil {
		directory, err := f.allocate(nil, createTraceID(ctx))
		if err != nil {
			return nil, err
		}
		return f.connect(ctx, directory, nil, f.readiness)
	}
	directory, ok, err := f.existingFor(ctx, *taskID, true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("Task is outside Filo creation scope")
	}
	return f.connect(ctx, directory, taskID, f.readiness)
}

// Follow reconnects an existing owned executor for read-only browsing without
// ever allocating a native writer.
func (f *TaskExecutorFactory) Follow(ctx context.Context, taskID string) (*codex.CodexHost, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if f.isStopped() {
		return nil, errors.New("Filo executor factory is closing")
	}
	owned, err := f.Owns(taskID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, nil
	}
	directory, ok, err := f.existing(taskID, false)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	conn, err := f.connect(ctx, directory, &taskID, min(f.readiness, executorFollowReadiness))
	if err == nil {
		return conn, nil
	}
	// Completion can retire the writer between discovery and connection.
	// Confirm the current task reference ended before allowing the caller's
	// read-only history path. Still being stopped rethrows the original cause.
	if f.isStopped() {
		return nil, err
	}
	directory, ok, existErr := f.existing(taskID, false)
	if existErr != nil {
		return nil, existErr
	}
	if !ok || errors.Is(err, errExecutorUnavailable) {
		// A browsing read may use bounded native history when the recorded
		// executor cannot be reached. This never admits a replacement writer;
		// explicit mutations still go through Open and its ownership checks.
		return nil, nil
	}
	return nil, err
}

var errExecutorUnavailable = errors.New("Filo native executor is not ready")

// Close fences new work. It never signals a native process: an in-flight
// connection is closed by the caller that receives it.
func (f *TaskExecutorFactory) Close() { f.setStopped() }

// allocate creates one executor directory with its private configuration and
// state, publishes the selected attempt and only then launches the native
// process.
func (f *TaskExecutorFactory) allocate(taskID *string, createTrace string) (string, error) {
	f.admission.Lock()
	defer f.admission.Unlock()
	if f.isStopped() {
		return "", errors.New("Filo executor factory is closing")
	}
	executors := filepath.Join(f.directory, "executors")
	if err := os.MkdirAll(executors, 0o700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(executors)
	if err != nil {
		return "", err
	}
	active := 0
	for _, entry := range entries {
		name := entry.Name()
		if !taskIDPattern.MatchString(name) {
			continue
		}
		directory := filepath.Join(executors, name)
		value, err := ReadExecutorFile(directory, executorStateName)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
			// A crash before configuration or state publication must not poison
			// future creation. A started claim without state stays ambiguous and
			// is never treated as released.
			if _, claimErr := ReadExecutorFile(directory, executorClaimName); claimErr == nil {
				active++
			} else if !errors.Is(claimErr, fs.ErrNotExist) {
				return "", claimErr
			}
			continue
		}
		state, err := ParseExecutorState(value)
		if err != nil {
			return "", err
		}
		if state.Phase != "retired" && state.Phase != "exited" &&
			(processAlive(state.KeeperPid) || (state.NativePid != nil && processAlive(*state.NativePid))) {
			active++
		}
	}
	if active >= f.capacity {
		return "", errors.New("Filo native executor capacity is reached")
	}
	executorID, err := randomUUID()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(executors, executorID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", err
	}
	token, err := executorSecret()
	if err != nil {
		return "", err
	}
	nativeToken, err := executorSecret()
	if err != nil {
		return "", err
	}
	config := ExecutorConfig{
		Token:       token,
		NativeToken: nativeToken,
		Workspace:   f.workspace,
		TaskID:      taskID,
		CreateTrace: createTrace,
	}
	if err := writeExclusive(filepath.Join(directory, executorConfigName), Marshal(config)); err != nil {
		return "", err
	}
	if err := SaveExecutorState(directory, ExecutorState{Phase: "starting", KeeperPid: os.Getpid(), TaskID: taskID}); err != nil {
		return "", err
	}
	// Publish the selected attempt before launching: a restart must inspect this
	// identity instead of inventing another writer. The task directory already
	// exists, because admission recorded its origin before this attempt.
	if taskID != nil {
		task := filepath.Join(f.directory, "tasks", *taskID)
		if err := os.MkdirAll(task, 0o700); err != nil {
			return "", err
		}
		if err := SaveExecutorFile(task, executorCurrentName, map[string]string{"executorId": executorID}); err != nil {
			return "", err
		}
		created, err := taskScope(f.directory, *taskID, false)
		if err != nil {
			return "", err
		}
		if !created {
			if err := SaveExecutorFile(task, executorRecoveryName, map[string]string{"executorId": executorID}); err != nil {
				return "", err
			}
		}
	}
	if err := f.launch(directory); err != nil {
		_ = SaveExecutorState(directory, ExecutorState{Phase: "exited", KeeperPid: os.Getpid(), TaskID: taskID})
		return "", err
	}
	return directory, nil
}

// existing locates the executor directory of one owned task, optionally
// allocating a replacement writer when the recorded attempt already ended.
func (f *TaskExecutorFactory) existing(taskID string, mayLaunch bool) (string, bool, error) {
	return f.existingFor(context.Background(), taskID, mayLaunch)
}

func (f *TaskExecutorFactory) existingFor(ctx context.Context, taskID string, mayLaunch bool) (string, bool, error) {
	created, err := taskScope(f.directory, taskID, false)
	if err != nil {
		return "", false, err
	}
	mayAllocate := mayLaunch && (created || sessions.IdleRecoveryAllowed(ctx, taskID))
	owned, err := f.Owns(taskID)
	if err != nil {
		return "", false, err
	}
	if !owned {
		if !mayAllocate {
			return "", false, nil
		}
		directory, err := f.allocate(&taskID, "")
		return directory, err == nil, err
	}
	task := filepath.Join(f.directory, "tasks", taskID)
	reference, err := ReadExecutorFile(task, executorCurrentName)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		reference, err = ReadExecutorFile(task, executorOriginName)
		if errors.Is(err, fs.ErrNotExist) {
			reference, err = ReadExecutorFile(task, executorRecoveryName)
		}
		if err != nil {
			return "", false, err
		}
	}
	executorID, err := ExecutorReference(reference)
	if err != nil {
		return "", false, err
	}
	directory := filepath.Join(f.directory, "executors", executorID)
	value, err := ReadExecutorFile(directory, executorStateName)
	if err != nil {
		return "", false, err
	}
	state, err := ParseExecutorState(value)
	if err != nil {
		return "", false, err
	}
	if state.TaskID == nil || *state.TaskID != taskID {
		return "", false, errors.New("Executor task identity does not match")
	}
	if state.Phase == "retired" || state.Phase == "exited" || executorWriterEnded(state) && sessions.IdleRecoveryAllowed(ctx, taskID) {
		if !mayAllocate {
			return "", false, nil
		}
		directory, err := f.allocate(&taskID, "")
		if err != nil {
			return "", false, err
		}
		return directory, true, nil
	}
	// Missing readiness or a dead keeper is not proof that the native writer
	// ended, so the directory is still returned.
	return directory, true, nil
}

// connect waits for a published endpoint and joins it. Readiness retries never
// create another native writer and never replay input.
func (f *TaskExecutorFactory) connect(ctx context.Context, directory string, taskID *string, readiness time.Duration) (*codex.CodexHost, error) {
	value, err := ReadExecutorFile(directory, executorConfigName)
	if err != nil {
		return nil, err
	}
	config, err := ParseExecutorConfig(value)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(readiness)
	for !f.isStopped() && time.Now().Before(deadline) {
		stateValue, err := ReadExecutorFile(directory, executorStateName)
		if err != nil {
			return nil, err
		}
		state, err := ParseExecutorState(stateValue)
		if err != nil {
			return nil, err
		}
		if taskID != nil && (state.TaskID == nil || *state.TaskID != *taskID) {
			return nil, errors.New("Executor task identity does not match")
		}
		if state.Phase == "exited" || state.Phase == "retired" {
			return nil, errors.New("Native executor ended; retry explicitly")
		}
		if state.Phase == "ready" && state.URL != nil {
			remaining := time.Until(deadline)
			if remaining < time.Millisecond {
				remaining = time.Millisecond
			}
			conn, err := codex.ConnectCodexHost(ctx, *state.URL, codex.HostOptions{
				Token:   &config.Token,
				Timeout: remaining,
			})
			if err == nil {
				if !f.isStopped() {
					return conn, nil
				}
				conn.Close()
			}
		}
		if !processAlive(state.KeeperPid) {
			return nil, errors.New("Native executor keeper is unavailable; its task has not been replayed")
		}
		time.Sleep(executorReadinessPoll)
	}
	return nil, errExecutorUnavailable
}

func (f *TaskExecutorFactory) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *TaskExecutorFactory) setStopped() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}

// executorSecret mirrors randomBytes(32).toString('hex'): one 256 bit private
// credential in its published lowercase hexadecimal form.
func executorSecret() (string, error) {
	var material [32]byte
	if _, err := rand.Read(material[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(material[:]), nil
}

// launchExecutorRunner starts the runner as a detached copy of this binary, so
// the native executor outlives the request that created it.
func launchExecutorRunner(directory string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, executorRunnerArgument, directory)
	command.SysProcAttr = detachedProcessAttributes()
	if err := command.Start(); err != nil {
		return err
	}
	// Release the detached child without waiting for its lifetime to end.
	return command.Process.Release()
}
