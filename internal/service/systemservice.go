package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/sessions"
	"github.com/newo-ether/filo/internal/uploads"
	"github.com/newo-ether/filo/internal/windowsapp"
)

// The desktop IPC gateway is the service the original Codex desktop launches as
// one Windows service account. It owns no native history of its own: it forwards
// desktop sessions, and it reads the selected original account through the
// private user helper credential recorded next to it.
const (
	// systemServiceDirectoryRefusal rejects a directory that is missing or not
	// absolute, so a relative path can never resolve against a working directory
	// the service manager chose.
	systemServiceDirectoryRefusal = "Provide an absolute private Filo service directory"
	// systemServiceBindRefusal rejects a published or malformed bind/port pair.
	systemServiceBindRefusal = "Invalid Filo bind/port"
	// systemServiceSIDRefusal rejects a missing original desktop account SID.
	systemServiceSIDRefusal = "Missing or invalid Filo desktop account SID"
	// systemServiceCredentialRefusal rejects a helper credential path that is
	// missing or not absolute.
	systemServiceCredentialRefusal = "Invalid Filo helper credential path"
	// systemServiceDesktopUnavailable is the single refusal of the desktop
	// bridge. The TypeScript entry point raises it after its verification script
	// has already tried to dial the pipe; Go dials and verifies in one step, so a
	// missing pipe and a rejected account/application identity answer alike.
	systemServiceDesktopUnavailable = "Original Codex desktop is unavailable or its Windows account/application identity could not be verified"
	// systemServiceDiagnosticPrefix leads the one startup failure line.
	systemServiceDiagnosticPrefix = "Filo system service failed: "
	// systemServiceDesktopTimeout bounds one desktop pipe dial, the same 5000 ms
	// the TypeScript entry point passes to its IPC client.
	systemServiceDesktopTimeout = 5 * time.Second
)

// systemServiceSIDPattern admits the two account families an original desktop can
// belong to, exactly as the TypeScript entry point requires by regular
// expression.
var systemServiceSIDPattern = regexp.MustCompile(`^S-1-(?:5-21|12-1)-[0-9-]+$`)

// systemServiceOptions configures one desktop IPC gateway entry point.
type systemServiceOptions struct {
	// Directory is the private service directory holding config.json and token.
	Directory string
	// PipePath overrides the desktop IPC pipe. Empty selects the production pipe
	// a deployment uses; a test names one of its own.
	PipePath string
	// Output receives the ready record. Nil selects standard output.
	Output io.Writer
	// Error receives the one startup failure line. Nil selects standard error.
	Error io.Writer
}

// RunSystemService runs the desktop IPC gateway entry point. It returns the
// process exit code: 0 after a requested shutdown, 1 when startup failed.
func RunSystemService(arguments []string, stderr io.Writer) int {
	var directory string
	if len(arguments) > 0 {
		directory = arguments[0]
	}
	return runSystemService(systemServiceOptions{Directory: directory, Error: stderr})
}

// runSystemService admits one gateway and reports its failure as one line.
func runSystemService(options systemServiceOptions) int {
	if err := serveSystemService(options); err != nil {
		writeEntryDiagnostic(options.Error, systemServiceDiagnosticPrefix, err.Error())
		return 1
	}
	return 0
}

// systemServiceReady is the one record a launcher reads from standard output. The
// field order matches the TypeScript `JSON.stringify` key order.
type systemServiceReady struct {
	Service string `json:"service"`
	Device  string `json:"device"`
	Mode    string `json:"mode"`
	Port    int    `json:"port"`
}

// serveSystemService admits one desktop IPC gateway and blocks until it stops. A
// startup failure is returned after every resource it created is released, which
// is what the TypeScript `catch` around `listen` does.
func serveSystemService(options systemServiceOptions) error {
	if options.Directory == "" || !filepath.IsAbs(options.Directory) {
		return errors.New(systemServiceDirectoryRefusal)
	}
	config, err := readEntryJSON(filepath.Join(options.Directory, entryConfigName))
	if err != nil {
		return err
	}
	bind, port, admitted := readEntryBinding(config)
	if !admitted {
		return errors.New(systemServiceBindRefusal)
	}
	sid, _ := config["desktopSid"].(string)
	if !systemServiceSIDPattern.MatchString(sid) {
		return errors.New(systemServiceSIDRefusal)
	}
	token, err := readEntryText(filepath.Join(options.Directory, entryTokenName))
	if err != nil {
		return err
	}
	// The selected original account is resolved when a read needs it, and the
	// resolution is remembered until it succeeds.
	metadata := history.NewNativeHistory(nativeHome(sid))
	workerTokenPath, _ := config["workerTokenPath"].(string)
	if !filepath.IsAbs(workerTokenPath) {
		return errors.New(systemServiceCredentialRefusal)
	}
	workerURL, _ := config["workerUrl"].(string)
	worker, err := NewUserWorker(workerURL, UserWorkerTokenFunc(func(context.Context) (string, error) {
		return readEntryText(workerTokenPath)
	}), metadata, filepath.Join(filepath.Dir(workerTokenPath), serviceTaskDirectoryName), DefaultUserWorkerTimeouts)
	if err != nil {
		return err
	}
	attachments, err := uploads.New(filepath.Join(filepath.Dir(workerTokenPath), "uploads"))
	if err != nil {
		worker.Close()
		return err
	}
	defer attachments.Close()
	hub := NewHub()
	holder := &desktopSurface{}
	pipePath := options.PipePath
	if pipePath == "" {
		pipePath = desktop.DefaultPipePath
	}
	client := desktop.NewClient(desktop.Options{
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			connection, err := desktop.DialPipe(ctx, pipePath, func(verify context.Context, pid uint32) error {
				if _, err := windowsapp.Inspect(verify, pid, sid); err != nil {
					return errors.New(systemServiceDesktopUnavailable)
				}
				return nil
			})
			if err != nil {
				return nil, errors.New(systemServiceDesktopUnavailable)
			}
			return connection, nil
		},
		Timeout: systemServiceDesktopTimeout,
		OnBroadcast: func(message desktop.Message) {
			if surface := holder.get(); surface != nil {
				surface.Receive(message)
			}
		},
		OnClosed: func() {
			if surface := holder.get(); surface != nil {
				surface.Disconnected()
			}
		},
	})
	var created sessions.CreatedReader
	if tasks := worker.NewTasks(); tasks != nil {
		created = createdScope{tasks: tasks}
	}
	surface := sessions.New(sessions.Options{
		IPC:     client,
		History: worker,
		Base:    worker,
		Created: created,
		Hooks:   sessions.Hooks{Changed: hub.Changed, Closed: hub.Reset},
	})
	holder.set(surface)
	worker.SetChangedHandler(surface.SourceChanged)

	var httpServer *http.Server
	shutdown := func() {
		hub.Close()
		surface.Close()
		worker.Close()
		client.Close()
		if httpServer != nil {
			closeContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(closeContext)
			_ = httpServer.Close()
		}
	}
	latch := newExitLatch()
	server, err := NewPublicServer(ServerOptions{
		Sessions:      surface,
		Uploads:       attachments,
		Usage:         worker.Usage,
		Token:         token,
		Events:        hub,
		OnShutdown:    func() { latch.stop(0, shutdown) },
		CreateLogPath: filepath.Join(options.Directory, createSystemLogName),
		DesktopStatus: func() DesktopStatus {
			// Discovery is asynchronous. Desktop absence must never block service
			// health, so the request only starts one attempt.
			go func() { _ = client.Connect(context.Background()) }()
			return desktopStatusOf(client.ConnectionStatus())
		},
	})
	if err != nil {
		shutdown()
		return err
	}
	httpServer = &http.Server{Handler: server, ReadHeaderTimeout: entryHeaderTimeout, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		shutdown()
		return err
	}
	go func() { _ = httpServer.Serve(listener) }()
	device, _ := os.Hostname()
	writeEntryRecord(options.Output, systemServiceReady{
		Service: "Filo",
		Device:  device,
		Mode:    "desktop-ipc",
		Port:    port,
	})
	signals, cancelSignals := entrySignals()
	defer cancelSignals()
	go func() {
		for range signals {
			latch.stop(0, shutdown)
		}
	}()
	<-latch.done
	return nil
}

// desktopSurface lets the desktop client keep the callbacks it copies at
// construction while the session surface they report to is created afterwards,
// which is the one ordering these two types force.
type desktopSurface struct {
	mu      sync.RWMutex
	current *sessions.DesktopSessions
}

func (holder *desktopSurface) set(surface *sessions.DesktopSessions) {
	holder.mu.Lock()
	holder.current = surface
	holder.mu.Unlock()
}

func (holder *desktopSurface) get() *sessions.DesktopSessions {
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return holder.current
}

// nativeHome resolves one original account home once and remembers it, which is
// the memo the TypeScript entry point keeps in front of its script call. A failed
// resolution is not remembered, so the next read tries again.
func nativeHome(sid string) func(context.Context) (string, error) {
	var mu sync.Mutex
	var home string
	resolved := false
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if resolved {
			return home, nil
		}
		value, err := windowsapp.NativeHome(ctx, sid)
		if err != nil {
			return "", err
		}
		home, resolved = value, true
		return value, nil
	}
}

// desktopStatusOf reports one desktop IPC status in the /v1/info shape.
func desktopStatusOf(status desktop.Status) DesktopStatus {
	value := DesktopStatus{State: status.State}
	if status.Error != "" {
		message := status.Error
		value.Error = &message
	}
	return value
}

var _ SessionAPI = (*sessions.DesktopSessions)(nil)
