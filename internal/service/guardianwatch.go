package service

import (
	"errors"
	"github.com/newo-ether/filo/internal/nativejson"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var errGuardianState = errors.New("Invalid native lifetime state")

type guardianProcess interface {
	alive() bool
	release() error
	close()
}

type guardianWatch struct {
	directory, task, terminal string
	state                     map[string]any
	keeper, native            guardianProcess
	mu                        sync.Mutex
	ready                     bool
}

func (g *guardianWatch) notification(packet map[string]any) {
	method, _ := packet["method"].(string)
	if method != "turn/started" && method != "turn/completed" {
		return
	}
	params, _ := nativejson.Fields(packet["params"])
	id, _ := params["threadId"].(string)
	g.mu.Lock()
	defer g.mu.Unlock()
	if id != g.task {
		return
	}
	if method == "turn/started" {
		g.terminal = ""
		return
	}
	turn, _ := nativejson.Fields(params["turn"])
	status, _ := turn["status"].(string)
	if status == "completed" || status == "failed" || status == "interrupted" {
		g.terminal, _ = turn["id"].(string)
	}
}

func (g *guardianWatch) identity() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.task
}

func (g *guardianWatch) bind(task string) error {
	if !taskIDPattern.MatchString(task) {
		return errGuardianState
	}
	g.mu.Lock()
	g.task, g.terminal = task, ""
	g.mu.Unlock()
	g.state["taskId"] = task
	return nil
}

func (g *guardianWatch) publish() error {
	record := map[string]any{"pid": os.Getpid()}
	if task := g.identity(); task != "" {
		record["taskId"] = task
	}
	return SaveExecutorFile(g.directory, guardianReadyName, record)
}

func guardianTask(reply map[string]any, id string) (map[string]any, error) {
	thread, ok := nativejson.Fields(reply["thread"])
	if !ok || thread["id"] != id {
		return nil, errGuardianState
	}
	return thread, nil
}

func (g *guardianWatch) idle(thread map[string]any) bool {
	status, _ := nativejson.Fields(thread["status"])
	kind, _ := status["type"].(string)
	g.mu.Lock()
	defer g.mu.Unlock()
	return kind == "idle" || kind == "notLoaded" || kind == "systemError" && g.terminal != ""
}

type guardianRequest func(string, any) (map[string]any, error)

// step never releases a process while the keeper is alive, native state is
// unknown, or a native turn remains active. Handles, not a reused PID, own release.
func (g *guardianWatch) step(request guardianRequest) (bool, error) {
	if g.identity() == "" && g.keeper.alive() {
		value, err := ReadExecutorFile(g.directory, executorStateName)
		if err != nil {
			return false, err
		}
		latest, ok := nativejson.Fields(value)
		if !ok {
			return false, errGuardianState
		}
		if raw := latest["taskId"]; raw != nil {
			id, ok := raw.(string)
			if !ok || !taskIDPattern.MatchString(id) {
				return false, errGuardianState
			}
			reply, err := request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
			if err != nil {
				return false, err
			}
			if _, err = guardianTask(reply, id); err != nil {
				return false, err
			}
			if err = g.bind(id); err != nil {
				return false, err
			}
			if err = g.publish(); err != nil {
				return false, err
			}
		}
	}
	if g.keeper.alive() {
		return false, nil
	}
	if g.identity() == "" {
		listed, err := request("thread/loaded/list", map[string]any{})
		if err != nil {
			return false, err
		}
		ids, ok := listed["data"].([]any)
		if !ok || len(ids) > 1 {
			return false, errGuardianState
		}
		if len(ids) == 1 {
			id, ok := ids[0].(string)
			if !ok || !taskIDPattern.MatchString(id) {
				return false, errGuardianState
			}
			if _, err := request("thread/resume", map[string]any{"threadId": id, "excludeTurns": true}); err != nil {
				return false, err
			}
			if err := g.bind(id); err != nil {
				return false, err
			}
		}
	}
	idle := g.identity() == ""
	if id := g.identity(); id != "" {
		reply, err := request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
		if err != nil {
			return false, err
		}
		thread, err := guardianTask(reply, id)
		if err != nil {
			return false, err
		}
		idle = g.idle(thread)
	}
	if !idle {
		return false, nil
	}
	if g.native.alive() {
		if err := g.native.release(); err != nil {
			return false, err
		}
	}
	for g.native.alive() {
		time.Sleep(50 * time.Millisecond)
	}
	g.state["phase"] = "retired"
	delete(g.state, "url")
	// Files may already be absent; successful native release remains successful.
	_ = SaveExecutorFile(g.directory, executorStateName, g.state)
	return true, nil
}

func runGuardian(directory string, open func(int, bool) (guardianProcess, error)) error {
	if !filepath.IsAbs(directory) {
		return errGuardianState
	}
	configValue, err := ReadExecutorFile(directory, executorConfigName)
	if err != nil {
		return err
	}
	stateValue, err := ReadExecutorFile(directory, executorStateName)
	if err != nil {
		return err
	}
	config, ok := configValue.(map[string]any)
	if !ok {
		return errGuardianState
	}
	state, ok := stateValue.(map[string]any)
	if !ok {
		return errGuardianState
	}
	url, _ := state["nativeUrl"].(string)
	token, _ := config["nativeToken"].(string)
	keeperPID, keeperOK := jsPid(state["keeperPid"])
	nativePID, nativeOK := jsPid(state["nativePid"])
	if !loopbackEndpoint(url) || !tokenPattern.MatchString(token) ||
		!keeperOK || !nativeOK || keeperPID == nativePID {
		return errGuardianState
	}
	keeper, err := open(keeperPID, false)
	if err != nil {
		return err
	}
	defer keeper.close()
	native, err := open(nativePID, true)
	if err != nil {
		return err
	}
	defer native.close()
	if !keeper.alive() || !native.alive() {
		return errGuardianState
	}
	g := &guardianWatch{directory: directory, state: state, keeper: keeper, native: native}
	if raw := state["taskId"]; raw != nil {
		id, ok := raw.(string)
		if !ok {
			return errGuardianState
		}
		if err := g.bind(id); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for native.alive() {
		err := g.connection(url, token)
		if err != nil && !g.ready && (!keeper.alive() || !time.Now().Before(deadline)) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func (g *guardianWatch) connection(url, token string) error {
	client, err := connectGuardian(url, token, g.notification)
	if err != nil {
		return err
	}
	defer client.Close()
	if id := g.identity(); id != "" {
		if _, err := client.request("thread/resume", map[string]any{"threadId": id, "excludeTurns": true}); err != nil {
			return err
		}
	}
	if err := g.publish(); err != nil && !g.ready {
		return err
	}
	g.ready = true
	for g.native.alive() && !client.closed.Load() {
		retired, err := g.step(client.request)
		if err != nil {
			return err
		}
		if retired {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
