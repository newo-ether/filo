package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

const cliSharedOwnerConfigName = "owner.json"

type cliSharedOwnerConfig struct {
	Runtime   string   `json:"runtime"`
	Workspace string   `json:"workspace"`
	Token     string   `json:"token"`
	Lease     string   `json:"lease"`
	Arguments []string `json:"arguments"`
}

// RunCLISharedOwner owns only the native child it starts. Losing the wrapper
// fences discovery. Once exposed, the native child is never forcibly terminated:
// a read of idle state cannot atomically exclude another accepted native turn.
func RunCLISharedOwner(directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("Invalid shared CLI owner directory")
	}
	value, err := ReadExecutorFile(directory, cliSharedOwnerConfigName)
	if err != nil {
		return err
	}
	body, err := json.Marshal(value)
	var config cliSharedOwnerConfig
	if err == nil {
		err = json.Unmarshal(body, &config)
	}
	if err != nil {
		return err
	}
	if !filepath.IsAbs(config.Runtime) || !absoluteWorkspace(config.Workspace) ||
		!tokenPattern.MatchString(config.Token) || !validCLISharedLeaseURL(config.Lease) ||
		validateSharedCLIArguments(config.Arguments) != nil {
		return errors.New("Invalid shared CLI owner configuration")
	}
	defer os.RemoveAll(directory)
	record := cliSharedRecord{Token: config.Token, Lease: config.Lease, Workspace: config.Workspace}
	if !cliSharedLease(context.Background(), record) {
		return codexcliUnavailable()
	}
	tokenPath := filepath.Join(directory, "native-token")
	if err := writeExclusive(tokenPath, []byte(config.Token)); err != nil {
		return err
	}
	endpoint, err := reserveExecutorURL()
	if err != nil {
		return err
	}
	arguments := append(append([]string{}, config.Arguments...), "app-server", "--listen", endpoint,
		"--ws-auth", "capability-token", "--ws-token-file", tokenPath)
	command := exec.Command(config.Runtime, arguments...)
	command.Dir, command.Env = config.Workspace, nativeProcessEnvironment(config.Runtime)
	command.SysProcAttr = detachedProcessAttributes()
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() { _ = command.Wait(); close(exited) }()
	exposed := false
	defer func() {
		if !exposed {
			_ = command.Process.Kill()
			<-exited
		}
	}()
	var host *codex.CodexHost
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		host, err = codex.ConnectCodexHost(context.Background(), endpoint,
			codex.HostOptions{Token: &config.Token, Timeout: time.Second})
		if err == nil {
			break
		}
		select {
		case <-exited:
			return errors.New("Native shared app-server exited during startup")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if host == nil || host.CodexHome == "" {
		if host != nil {
			host.Close()
		}
		return errors.New("Native shared app-server did not become ready")
	}
	record.URL, record.Home = endpoint, host.CodexHome
	// Publication can become visible even if the write later returns an error.
	// From this point, uncertain native lifetime must be preserved.
	exposed = true
	if err := SaveExecutorFile(directory, cliSharedRecordName, record); err != nil {
		host.Close()
		return err
	}
	defer host.Close()
	for {
		select {
		case <-exited:
			return nil
		case <-time.After(250 * time.Millisecond):
		}
		if cliSharedLease(context.Background(), record) {
			continue
		}
		_ = os.Remove(filepath.Join(directory, cliSharedRecordName))
		host.Close()
		// Native websocket mode has no qualified atomic drain/exit API. Retain
		// this exact child until its own exit, including native SessionEnd hooks.
		<-exited
		return nil
	}
}
