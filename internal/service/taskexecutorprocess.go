package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

const (
	// executorNativeTokenName is the private credential file the native app
	// server reads instead of receiving the token in its command line.
	executorNativeTokenName = "native-token"
	// executorAppServerArgument is the native subcommand that listens on the
	// reserved loopback endpoint.
	executorAppServerArgument = "app-server"
	// executorStartupDeadline bounds the first published readiness. A native
	// child that never became reachable within this budget is released instead
	// of being retried forever.
	executorStartupDeadline = 30 * time.Second
	// executorReleaseGrace is the fallback kill delay after a retirement: the
	// executor has already verified terminal native state, so a child that
	// ignored its closed input is terminated rather than kept.
	executorReleaseGrace = 1500 * time.Millisecond
	// executorReconnectDelay is the TS `delay(500)` between reconnects.
	executorReconnectDelay = 500 * time.Millisecond
	// executorDesktopRuntimeScript resolves the installed desktop runtime, and
	// executorDesktopRuntimeTimeout bounds that PowerShell call.
	executorDesktopRuntimeScript  = "resolve-desktop-runtime.ps1"
	executorDesktopRuntimeTimeout = 60 * time.Second
	// executorRunnerDiagnostic is the only text a runner failure may print: no
	// credential, prompt or configuration text enters launcher diagnostics.
	executorRunnerDiagnostic = "Filo native task executor could not complete its lifecycle"
)

// ExecutorProcessOptions configures one executor lifecycle run.
//
// Divergence from the TS `runTaskExecutor(directory)` signature: every port that
// makes the run observable (the runtime resolver, the lifetime monitor, the
// retired-idle budget and the three timing budgets) is injectable, because a Go
// test cannot substitute a module-level import the way the TS fixture
// substitutes `node:child_process`. Zero values select the TS defaults.
type ExecutorProcessOptions struct {
	// Directory is the absolute private executor directory with its
	// configuration.
	Directory string
	// DesktopRuntime resolves the native app server executable. Nil selects
	// `scripts/resolve-desktop-runtime.ps1`.
	DesktopRuntime func() (string, error)
	// Guardian publishes lifetime protection for the native child. Nil selects
	// the installed `tools/FiloBackground.exe` monitor.
	Guardian func(directory string) (*NativeGuardian, error)
	// Idle retires a task with no client traffic. Zero selects the codex default
	// of 120 seconds.
	Idle time.Duration
	// StartupDeadline bounds the first readiness. Zero selects 30 seconds.
	StartupDeadline time.Duration
	// ReleaseGrace is the fallback kill delay after a retirement. Zero selects
	// 1.5 seconds.
	ReleaseGrace time.Duration
	// ReconnectDelay separates reconnect attempts. Zero selects 500ms.
	ReconnectDelay time.Duration
}

// RunTaskExecutor runs the complete lifecycle of one private native task
// executor in its own ordinary-user process, outside the gateway supervisor's
// child lifetime.
func RunTaskExecutor(directory string) error {
	return RunTaskExecutorWith(ExecutorProcessOptions{Directory: directory})
}

// RunTaskExecutorWith runs one executor lifecycle with explicit ports.
func RunTaskExecutorWith(options ExecutorProcessOptions) error {
	if !filepath.IsAbs(options.Directory) {
		return errors.New("Executor directory must be absolute")
	}
	directory := options.Directory
	value, err := ReadExecutorFile(directory, executorConfigName)
	if err != nil {
		return err
	}
	config, err := ParseExecutorConfig(value)
	if err != nil {
		return err
	}
	// Exclusive admission before native launch. A stale record is investigated,
	// never silently overwritten.
	if err := writeExclusive(filepath.Join(directory, executorClaimName),
		[]byte(strconv.Itoa(os.Getpid()))); err != nil {
		return err
	}
	lifecycle := &executorLifecycle{state: ExecutorState{
		Phase:     "starting",
		KeeperPid: os.Getpid(),
		TaskID:    config.TaskID,
	}}
	if err := lifecycle.save(directory); err != nil {
		return err
	}
	desktopRuntime := options.DesktopRuntime
	if desktopRuntime == nil {
		desktopRuntime = resolveDesktopRuntime
	}
	executable, err := desktopRuntime()
	if err != nil {
		return err
	}
	nativeURL, err := reserveExecutorURL()
	if err != nil {
		return err
	}
	tokenPath := filepath.Join(directory, executorNativeTokenName)
	if err := writeExclusive(tokenPath, []byte(config.NativeToken)); err != nil {
		return err
	}
	command, err := startNativeAppServer(executable, config.Workspace, nativeURL, tokenPath)
	if err != nil {
		return err
	}
	// The native child is never waited for by a caller: its exit is observed
	// here so that readiness, retirement and release all agree on it.
	var ended atomic.Bool
	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		ended.Store(true)
		close(exited)
	}()
	nativePid := command.Process.Pid
	lifecycle.mutate(func(state *ExecutorState) {
		state.NativePid = &nativePid
		state.NativeURL = &nativeURL
	})
	if err := lifecycle.save(directory); err != nil {
		// The broker has not been exposed and no task or input can exist in this
		// child yet.
		endNativeProcess(command, &ended, exited)
		return err
	}
	guardianFactory := options.Guardian
	if guardianFactory == nil {
		guardianFactory = func(directory string) (*NativeGuardian, error) {
			return StartNativeGuardian(NativeGuardianOptions{Directory: directory})
		}
	}
	guardian, guardianErr := guardianFactory(directory)
	if guardianErr == nil {
		guardianPid := guardian.PID
		lifecycle.mutate(func(state *ExecutorState) { state.GuardianPid = &guardianPid })
		if err := lifecycle.save(directory); err != nil {
			guardianErr = err
		}
	}
	if guardianErr != nil {
		// Protection was not exposed and no task or input exists in this native
		// child yet.
		endNativeProcess(command, &ended, exited)
		lifecycle.mutate(func(state *ExecutorState) { state.Phase = "exited" })
		if err := lifecycle.save(directory); err != nil {
			return err
		}
		return guardianErr
	}
	run := &executorRun{
		directory: directory,
		config:    config,
		nativeURL: nativeURL,
		command:   command,
		ended:     &ended,
		exited:    exited,
		lifecycle: lifecycle,
		guardian:  guardian,
		grace:     options.releaseGrace(),
		idle:      options.Idle,
	}
	deadline := time.Now().Add(options.startupDeadline())
	var published bool
	for !ended.Load() && !lifecycle.retiring.Load() {
		err := run.attempt(&published)
		if err != nil && !published && !time.Now().Before(deadline) {
			// No input is exposed before readiness, so a failed first handshake
			// can release this empty child instead of retrying forever.
			endNativeProcess(command, &ended, exited)
			return err
		}
		if !ended.Load() && !lifecycle.retiring.Load() {
			lifecycle.mutate(func(state *ExecutorState) {
				state.Phase = "disconnected"
				state.URL = nil
			})
			// A state-storage outage must not remove the accepted task's
			// guardian, so the reconnect continues while storage recovers.
			_ = lifecycle.save(directory)
			waitForExit(exited, options.reconnectDelay())
		}
	}
	phase := "exited"
	if lifecycle.retiring.Load() {
		phase = "retired"
	}
	lifecycle.mutate(func(state *ExecutorState) {
		state.Phase = phase
		state.URL = nil
	})
	return lifecycle.save(directory)
}

// RunTaskExecutorRunner is the detached runner subcommand: the entry point of
// the child the factory launches. It returns the process exit code and prints at
// most one diagnostic.
func RunTaskExecutorRunner(arguments []string) int {
	if len(arguments) < 1 {
		fmt.Fprintln(os.Stderr, executorRunnerDiagnostic)
		return 1
	}
	if err := RunTaskExecutor(arguments[0]); err != nil {
		fmt.Fprintln(os.Stderr, executorRunnerDiagnostic)
		return 1
	}
	return 0
}

// executorLifecycle is the durable lifecycle record of one executor process,
// shared between the keeper loop and the callbacks the native task executor
// invokes concurrently.
type executorLifecycle struct {
	mu       sync.Mutex
	state    ExecutorState
	retiring atomic.Bool
}

func (l *executorLifecycle) mutate(change func(*ExecutorState)) {
	l.mu.Lock()
	change(&l.state)
	l.mu.Unlock()
}

func (l *executorLifecycle) snapshot() ExecutorState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *executorLifecycle) save(directory string) error {
	return SaveExecutorState(directory, l.snapshot())
}

func (l *executorLifecycle) taskID() *string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.TaskID == nil {
		return nil
	}
	value := *l.state.TaskID
	return &value
}

// executorRun is one published executor attempt: its accepted task, its private
// endpoint and the native child that backs it.
type executorRun struct {
	directory string
	config    ExecutorConfig
	nativeURL string
	command   *exec.Cmd
	ended     *atomic.Bool
	exited    chan struct{}
	lifecycle *executorLifecycle
	guardian  *NativeGuardian
	grace     time.Duration
	idle      time.Duration
}

// attempt connects to the native host once, publishes the private executor and
// waits for either the client side or the native child to end. After transport
// loss only this authenticated, already-owned host is reconnected, and no
// mutation is ever replayed: a resume reinstates the subscription to the same
// native task.
func (r *executorRun) attempt(published *bool) error {
	connection, err := codex.ConnectCodexHost(context.Background(), r.nativeURL,
		codex.HostOptions{Token: &r.config.NativeToken})
	if err != nil {
		return err
	}
	defer connection.Close()
	var closedOnce sync.Once
	closed := make(chan struct{})
	connection.Rpc.SetClosedHandler(func(error) { closedOnce.Do(func() { close(closed) }) })
	admitted := ""
	if id := r.lifecycle.taskID(); id != nil {
		admitted = *id
	}
	diagnostics := executorCreateDiagnostics(r.directory, r.config.CreateTrace)
	createContext := withCreateTrace(context.Background(), r.config.CreateTrace, diagnostics)
	executor, err := codex.NewTaskExecutor(connection.Rpc, codex.TaskExecutorOptions{
		Token:      r.config.Token,
		AdmittedID: admitted,
		ReleaseNative: func() error {
			r.lifecycle.retiring.Store(true)
			connection.Close()
			// The task executor has fenced new requests and independently
			// verified terminal native state, so only this exact owned child is
			// released and never a pid or name selected desktop.
			releaseNativeProcess(r.command, r.ended, r.exited, r.grace)
			return nil
		},
		RecordTask: func(id string) error {
			r.lifecycle.mutate(func(state *ExecutorState) { state.TaskID = &id })
			if err := r.lifecycle.save(r.directory); err != nil {
				recordCreateStage(createContext, createStageExecutorLifecycleFailed, err)
				return err
			}
			if err := RecordCreatedTask(r.directory, id); err != nil {
				recordCreateStage(createContext, createStageExecutorProvenanceFailed, err)
				return err
			}
			recordCreateStage(createContext, createStageExecutorProvenanceRecorded, nil)
			if err := r.guardian.Bind(id); err != nil {
				recordCreateStage(createContext, createStageExecutorGuardianFailed, err)
				return err
			}
			recordCreateStage(createContext, createStageExecutorGuardianBound, nil)
			return nil
		},
		OnNativeCreated: func() {
			recordCreateStage(createContext, createStageExecutorNativeAcknowledged, nil)
		},
		Idle:              r.idle,
		ProtectedLifetime: r.guardian.Alive,
	})
	if err != nil {
		return err
	}
	// Unknown native state keeps the writer alive for a later verification, so
	// the failure event is deliberately consumed without a reaction.
	executor.SetOnFailure(func(error) {})
	if *published {
		if taskID := r.lifecycle.taskID(); taskID != nil {
			if _, err := connection.Rpc.Request("thread/resume",
				map[string]any{"threadId": *taskID, "excludeTurns": true}); err != nil {
				return err
			}
		}
	}
	url, err := executor.Listen()
	if err != nil {
		return err
	}
	r.lifecycle.mutate(func(state *ExecutorState) {
		state.URL = &url
		state.Phase = "ready"
	})
	if err := r.lifecycle.save(r.directory); err != nil {
		return err
	}
	*published = true
	select {
	case <-closed:
	case <-r.exited:
	}
	if r.lifecycle.retiring.Load() {
		<-r.exited
	}
	return nil
}

// startNativeAppServer starts the native app server on the reserved endpoint.
// The credential reaches it through a private file, and the inherited
// environment loses every CODEX_APP_SERVER_ override.
func startNativeAppServer(executable, workspace, url, tokenPath string) (*exec.Cmd, error) {
	command := exec.Command(executable, executorAppServerArgument, "--listen", url,
		"--ws-auth", "capability-token", "--ws-token-file", tokenPath)
	command.Dir = workspace
	command.Env = nativeProcessEnvironment(executable)
	command.SysProcAttr = detachedProcessAttributes()
	// Native diagnostics cannot block tool execution or grow an unbounded log.
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command, nil
}

// nativeProcessEnvironment mirrors the TS environment preparation: every
// CODEX_APP_SERVER_ override is dropped and the native runtime directory leads
// PATH, so the child resolves its own runtime before any inherited installation.
func nativeProcessEnvironment(executable string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	path := ""
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(strings.ToUpper(key), "CODEX_APP_SERVER_") {
			continue
		}
		if found && strings.EqualFold(key, "PATH") {
			path = value
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "PATH="+filepath.Dir(executable)+";"+path)
}

// endNativeProcess ends this exact owned child immediately. Every caller has
// proven that the child cannot be kept: no task or input was ever exposed on it,
// so it is never trusted to observe a closed input.
func endNativeProcess(command *exec.Cmd, ended *atomic.Bool, exited chan struct{}) {
	if !ended.Load() {
		_ = command.Process.Kill()
	}
	waitForExit(exited, 0)
}

// releaseNativeProcess ends this exact owned child and waits for it, with a
// bounded kill fallback for a child that ignored its closed input.
func releaseNativeProcess(command *exec.Cmd, ended *atomic.Bool, exited chan struct{}, grace time.Duration) {
	if grace <= 0 {
		grace = executorReleaseGrace
	}
	timer := time.AfterFunc(grace, func() {
		if !ended.Load() {
			_ = command.Process.Kill()
		}
	})
	waitForExit(exited, 0)
	timer.Stop()
}

// waitForExit waits for the native child, optionally with a timeout.
func waitForExit(exited chan struct{}, timeout time.Duration) bool {
	if timeout <= 0 {
		<-exited
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exited:
		return true
	case <-timer.C:
		return false
	}
}

// reserveExecutorURL reserves a loopback endpoint for the native app server and
// releases it again: the native child binds the port itself.
func reserveExecutorURL() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return "", errors.New("Cannot allocate native endpoint")
	}
	if err := listener.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("ws://127.0.0.1:%d", address.Port), nil
}

// resolveDesktopRuntime asks the Windows-provided shell for the installed
// desktop runtime of the current user.
func resolveDesktopRuntime() (string, error) {
	script, err := deploymentPath("scripts", executorDesktopRuntimeScript)
	if err != nil {
		return "", err
	}
	stdout, err := windowsPowerShell(script, nil, executorDesktopRuntimeTimeout)
	if err != nil {
		return "", err
	}
	return parseDesktopRuntime(stdout)
}

// parseDesktopRuntime validates the resolver output: only an absolute
// executable is a usable runtime.
func parseDesktopRuntime(stdout string) (string, error) {
	var result struct {
		Executable *string `json:"Executable"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		return "", err
	}
	if result.Executable == nil || !filepath.IsAbs(*result.Executable) {
		return "", errors.New("Desktop runtime is unavailable")
	}
	return *result.Executable, nil
}

// startupDeadline is the first-readiness budget of one run.
func (o ExecutorProcessOptions) startupDeadline() time.Duration {
	if o.StartupDeadline > 0 {
		return o.StartupDeadline
	}
	return executorStartupDeadline
}

// releaseGrace is the fallback kill delay of one run.
func (o ExecutorProcessOptions) releaseGrace() time.Duration {
	if o.ReleaseGrace > 0 {
		return o.ReleaseGrace
	}
	return executorReleaseGrace
}

// reconnectDelay is the reconnect separation of one run.
func (o ExecutorProcessOptions) reconnectDelay() time.Duration {
	if o.ReconnectDelay > 0 {
		return o.ReconnectDelay
	}
	return executorReconnectDelay
}
