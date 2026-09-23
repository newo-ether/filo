package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// The standalone gateway is the preview service the operator starts by hand. It
// owns no original desktop: it talks to one native app-server URL and keeps a
// private lease so two gateways never share a directory.
const (
	// standaloneBindRefusal rejects a configuration without an explicit
	// loopback bind and a valid port.
	standaloneBindRefusal = "Private user helper requires a loopback bind and valid port"
	// standaloneTokenRefusal rejects a credential that is not 256 bits of
	// hexadecimal.
	standaloneTokenRefusal = "Invalid standalone token"
	// standaloneLeaseRefusal rejects a lease record this gateway never wrote.
	standaloneLeaseRefusal = "Invalid gateway lease"
	// standaloneRunningRefusal refuses a second gateway on one directory.
	standaloneRunningRefusal = "Standalone gateway is already running"
	// standaloneCancelledRefusal reports a startup that a cancellation request
	// interrupted. The exit code stays 0, because the operator asked for it.
	standaloneCancelledRefusal = "Filo startup was cancelled"
	// standaloneApprovalRefusal answers every native UI request. Standalone
	// sessions have no desktop approval owner, so an unsupported request must
	// never hang.
	standaloneApprovalRefusal = "Remote approval/input is unavailable in this preview"
	// standaloneDiagnosticPrefix leads the one startup failure line, which the
	// TypeScript entry point also prints before it stops.
	standaloneDiagnosticPrefix = "Filo standalone failed: "
	// standaloneLeaseName is the lease of one standalone directory.
	standaloneLeaseName = "gateway.pid"
	// standaloneCancellationName is the private shutdown request a launcher
	// writes when it wants this gateway to stop.
	standaloneCancellationName = "gateway.stop"
	// standalonePollInterval is the TS `setInterval(..., 250)` of the
	// cancellation request.
	standalonePollInterval = 250 * time.Millisecond
	// standaloneIdentitySuffix marks the device of a standalone worker, which
	// keeps it distinguishable from the original desktop in /v1/info.
	standaloneIdentitySuffix = " · Filo"
	// standaloneMaximumLeasePID bounds a recorded owner to the range an
	// operating system process identifier can actually occupy.
	standaloneMaximumLeasePID = 1<<31 - 1
)

// standaloneOptions configures one standalone gateway entry point.
type standaloneOptions struct {
	// Arguments are the entry point arguments. The first one names the private
	// standalone directory; an absent argument selects the default directory
	// under the current user profile.
	Arguments []string
	// Output receives the ready record. Nil selects standard output.
	Output io.Writer
	// Error receives the one startup failure line. Nil selects standard error.
	Error io.Writer
}

// RunStandalone runs the standalone gateway entry point. It returns the process
// exit code: 0 after a requested shutdown, including a cancelled startup, and 1
// when startup failed.
func RunStandalone(arguments []string, stderr io.Writer) int {
	return runStandalone(standaloneOptions{Arguments: arguments, Error: stderr})
}

// runStandalone admits one gateway, reports its failure as one line and returns
// the exit code of the caller that claimed the shutdown.
func runStandalone(options standaloneOptions) int {
	latch := newExitLatch()
	gateway := newStandaloneGateway()
	if err := serveStandalone(options, latch, gateway); err != nil {
		writeEntryDiagnostic(options.Error, standaloneDiagnosticPrefix, err.Error())
		latch.stop(1, gateway.shutdown)
	}
	return latch.wait()
}

// standaloneReady is the one record a launcher reads from standard output. The
// field order matches the TypeScript `JSON.stringify` key order.
type standaloneReady struct {
	Ready   bool   `json:"ready"`
	Mode    string `json:"mode"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// serveStandalone admits one standalone gateway and blocks until it stops. Every
// failure is returned to the caller, which owns the exit code, so a cancelled
// startup is not reported as a failure.
func serveStandalone(options standaloneOptions, latch *exitLatch, gateway *standaloneGateway) error {
	directory, err := standaloneDirectory(options.Arguments)
	if err != nil {
		return err
	}
	config, err := readEntryJSON(filepath.Join(directory, entryConfigName))
	if err != nil {
		return err
	}
	appServerURL, isText := config["appServerUrl"].(string)
	bind, port, admitted := readEntryBinding(config)
	if !isText || !admitted || bind != "127.0.0.1" {
		return errors.New(standaloneBindRefusal)
	}
	token, err := readEntryText(filepath.Join(directory, entryTokenName))
	if err != nil {
		return err
	}
	if !tokenPattern.MatchString(token) {
		return errors.New(standaloneTokenRefusal)
	}
	if err := claimStandaloneLease(filepath.Join(directory, standaloneLeaseName)); err != nil {
		return err
	}
	gateway.claimLease(filepath.Join(directory, standaloneLeaseName))
	// The cancellation request is observed before HTTP is open, so a launcher
	// that gave up on this gateway can always stop it.
	go pollStandaloneCancellation(directory, latch, gateway)
	connection, err := codex.ConnectCodexHost(context.Background(), appServerURL, codex.HostOptions{})
	if err != nil {
		return err
	}
	gateway.setHost(connection)
	if latch.stopping() {
		connection.Close()
		return errors.New(standaloneCancelledRefusal)
	}
	factory, err := NewTaskExecutorFactory(TaskExecutorFactoryOptions{
		Directory: filepath.Join(directory, serviceTaskDirectoryName),
		Workspace: processWorkingDirectory(),
	})
	if err != nil {
		return err
	}
	metadata := codex.NewNativeMetadata(connection.Rpc)
	sessions := NewTaskSessions(metadata,
		history.NewNativeHistory(codexHome), factory)
	gateway.setSessions(sessions)
	connection.Rpc.SetHandlers(func(packet map[string]any) {
		_ = connection.Rpc.RejectRequest(packet["id"], standaloneApprovalRefusal)
	}, nil)
	connection.Rpc.SetClosedHandler(func(error) {
		if !latch.stopping() {
			latch.stop(1, gateway.shutdown)
		}
	})
	if latch.stopping() {
		return errors.New(standaloneCancelledRefusal)
	}
	hub := NewHub()
	sessions.SetChangedHandler(hub.Changed)
	device, _ := os.Hostname()
	server, err := NewServer(ServerOptions{
		Sessions:         sessions,
		Usage:            func(context.Context) (protocol.AccountUsage, error) { return metadata.Usage() },
		Token:            token,
		Events:           hub,
		OnShutdown:       func() { latch.stop(0, gateway.shutdown) },
		Identity:         &ServiceIdentity{SessionMode: "standalone", Device: device + standaloneIdentitySuffix},
		CreateLogPath:    filepath.Join(directory, createHelperLogName),
		TrustCreateTrace: true,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: entryHeaderTimeout, MaxHeaderBytes: 16 << 10}
	gateway.setServer(httpServer)
	listener, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	go func() { _ = httpServer.Serve(listener) }()
	signals, cancelSignals := entrySignals()
	defer cancelSignals()
	go func() {
		for range signals {
			latch.stop(0, gateway.shutdown)
		}
	}()
	writeEntryRecord(options.Output, standaloneReady{
		Ready:   true,
		Mode:    "standalone",
		Address: bind,
		Port:    port,
	})
	<-latch.done
	return nil
}

// standaloneGateway owns the resources of one standalone entry point, which the
// cancellation poller, the native close handler and the HTTP shutdown request
// release together. Every field is created after the poller starts, so each read
// goes through the holder.
type standaloneGateway struct {
	mu       sync.RWMutex
	server   *http.Server
	sessions *TaskSessions
	host     *codex.CodexHost
	lease    string
	polling  chan struct{}
	polled   bool
	// checking is the TS `checkingCancellation` guard: one tick never overlaps
	// the request read of the previous tick.
	checking atomic.Bool
}

// newStandaloneGateway prepares one holder.
func newStandaloneGateway() *standaloneGateway {
	return &standaloneGateway{polling: make(chan struct{})}
}

// claimLease records the lease this gateway wrote and must release again.
func (gateway *standaloneGateway) claimLease(lease string) {
	gateway.mu.Lock()
	gateway.lease = lease
	gateway.mu.Unlock()
}

// setHost records the admitted native connection.
func (gateway *standaloneGateway) setHost(host *codex.CodexHost) {
	gateway.mu.Lock()
	gateway.host = host
	gateway.mu.Unlock()
}

// setSessions records the session surface, which a cancellation request may only
// drain once it exists.
func (gateway *standaloneGateway) setSessions(sessions *TaskSessions) {
	gateway.mu.Lock()
	gateway.sessions = sessions
	gateway.mu.Unlock()
}

// setServer records the HTTP server so a shutdown before `Serve` still closes it.
func (gateway *standaloneGateway) setServer(server *http.Server) {
	gateway.mu.Lock()
	gateway.server = server
	gateway.mu.Unlock()
}

// sessionSurface returns the surface if assembly already finished.
func (gateway *standaloneGateway) sessionSurface() *TaskSessions {
	gateway.mu.RLock()
	defer gateway.mu.RUnlock()
	return gateway.sessions
}

// shutdown stops the poller and releases every resource in the TypeScript `stop`
// order. It runs inline on the goroutine of the caller that claimed it and is
// safe to reach again from the native close handler it triggered itself.
func (gateway *standaloneGateway) shutdown() {
	gateway.mu.Lock()
	if !gateway.polled {
		gateway.polled = true
		close(gateway.polling)
	}
	server, sessions, host, lease := gateway.server, gateway.sessions, gateway.host, gateway.lease
	gateway.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	if sessions != nil {
		sessions.Close()
	}
	if host != nil {
		host.Close()
	}
	if lease != "" {
		_ = os.Remove(lease)
	}
}

// pollStandaloneCancellation watches the private cancellation request of one
// gateway. The launcher observes the same file before retrying, and an
// independent host ignores it.
func pollStandaloneCancellation(directory string, latch *exitLatch, gateway *standaloneGateway) {
	ticker := time.NewTicker(standalonePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-gateway.polling:
			return
		case <-ticker.C:
		}
		if latch.stopping() || !gateway.checking.CompareAndSwap(false, true) {
			continue
		}
		if standaloneCancelled(directory, gateway) {
			latch.stop(0, gateway.shutdown)
		}
		gateway.checking.Store(false)
	}
}

// standaloneCancelled reports whether one cancellation request may stop this
// gateway. A missing request leaves it running, and so does a request that cannot
// be prepared because an auxiliary task still holds its client.
func standaloneCancelled(directory string, gateway *standaloneGateway) bool {
	if _, err := os.ReadFile(filepath.Join(directory, standaloneCancellationName)); err != nil {
		return false
	}
	surface := gateway.sessionSurface()
	if surface == nil {
		return true
	}
	return surface.PrepareShutdown(context.Background()) == nil
}

// standaloneDirectory resolves the private directory of one gateway. A relative
// argument resolves against the working directory, exactly as the TypeScript
// `resolve` does, and an absent one selects the default under the current user
// profile.
func standaloneDirectory(arguments []string) (string, error) {
	if len(arguments) > 0 {
		return filepath.Abs(arguments[0])
	}
	profile, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(profile, ".filo", "standalone"), nil
}

// claimStandaloneLease refuses to start when a live gateway already owns the
// lease, clears a lease whose owner is gone and claims the file exclusively.
func claimStandaloneLease(path string) error {
	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		previous, valid := entryLeaseNumber(string(body))
		if !valid || previous <= 0 || previous > standaloneMaximumLeasePID {
			return errors.New(standaloneLeaseRefusal)
		}
		if processAlive(int(previous)) {
			return errors.New(standaloneRunningRefusal)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	return writeExclusive(path, []byte(strconv.Itoa(os.Getpid())))
}

// entryLeaseNumber reads the recorded owner of one standalone lease. The gateway
// writes exactly one decimal process identifier, so only that shape is admitted:
// the TypeScript `Number()` also read other spellings, and a lease nobody of ours
// wrote must never authorize a start.
func entryLeaseNumber(text string) (int64, bool) {
	trimmed := codex.TrimJSWhitespace(text)
	if trimmed == "" {
		return 0, false
	}
	for index := 0; index < len(trimmed); index++ {
		if trimmed[index] < '0' || trimmed[index] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// codexHome resolves the native home of the standalone sessions. An explicit
// CODEX_HOME wins even when it is empty and is then resolved against the working
// directory, which is what the TypeScript `??` operator does.
func codexHome(context.Context) (string, error) {
	if home, isSet := os.LookupEnv("CODEX_HOME"); isSet {
		return filepath.Abs(home)
	}
	profile, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(profile, ".codex"))
}

// processWorkingDirectory is the workspace every executor configuration records.
func processWorkingDirectory() string {
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	return directory
}
