package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	// maxExecutorFileBytes bounds one executor state or configuration read.
	maxExecutorFileBytes = 16384
	executorStateName    = "state.json"
	// executorOriginName records creation provenance, and executorCurrentName
	// records the selected executor attempt of one task.
	executorOriginName  = "origin.json"
	executorCurrentName = "current.json"
)

// taskIDPattern is the identity form shared by a task, its executor directory
// and the executor reference recorded on a created task.
var taskIDPattern = uuidPattern

// endpointPattern is the only native endpoint form the executors publish: a
// loopback websocket with an explicit port.
var endpointPattern = regexp.MustCompile(`^ws://127\.0\.0\.1:[0-9]+$`)

// ExecutorConfig is the private per-executor configuration an operator places
// in the executor directory. The two credentials are deliberately separate:
// one authenticates this service's own wire, the other the native app server.
type ExecutorConfig struct {
	Token       string  `json:"token"`
	NativeToken string  `json:"nativeToken"`
	Workspace   string  `json:"workspace"`
	TaskID      *string `json:"taskId,omitempty"`
	CreateTrace string  `json:"createTrace,omitempty"`
}

// ExecutorState is the durable lifecycle record a follower reads to find a
// running executor. A ready record always names both endpoints and the native
// process that owns them.
type ExecutorState struct {
	Phase       string  `json:"phase"`
	KeeperPid   int     `json:"keeperPid"`
	NativePid   *int    `json:"nativePid,omitempty"`
	GuardianPid *int    `json:"guardianPid,omitempty"`
	NativeURL   *string `json:"nativeUrl,omitempty"`
	URL         *string `json:"url,omitempty"`
	TaskID      *string `json:"taskId,omitempty"`
}

// ParseExecutorConfig validates the configuration shape before any native
// process may start. The two tokens must be distinct 256-bit hexadecimal
// strings and the workspace must be absolute.
func ParseExecutorConfig(value any) (ExecutorConfig, error) {
	var config ExecutorConfig
	object, ok := value.(map[string]any)
	if !ok {
		return config, errors.New("Invalid executor configuration")
	}
	token, tokenOK := object["token"].(string)
	nativeToken, nativeOK := object["nativeToken"].(string)
	workspace, workspaceOK := object["workspace"].(string)
	if !tokenOK || !tokenPattern.MatchString(token) ||
		!nativeOK || !tokenPattern.MatchString(nativeToken) || token == nativeToken ||
		!workspaceOK || !absoluteWorkspace(workspace) {
		return config, errors.New("Invalid executor configuration")
	}
	taskID, present := object["taskId"]
	if present {
		text, isText := taskID.(string)
		if !isText || !taskIDPattern.MatchString(text) {
			return config, errors.New("Invalid executor configuration")
		}
		config.TaskID = &text
	}
	createTrace, present := object["createTrace"]
	if present {
		text, isText := createTrace.(string)
		if !isText || !createTracePattern.MatchString(text) {
			return config, errors.New("Invalid executor configuration")
		}
		config.CreateTrace = text
	}
	config.Token = token
	config.NativeToken = nativeToken
	config.Workspace = workspace
	return config, nil
}

// ParseExecutorState validates a decoded state record. A ready phase is only
// believable with both loopback endpoints and the native pid, so a truncated
// or hand-edited record is refused instead of driving a reconnect.
func ParseExecutorState(value any) (ExecutorState, error) {
	var state ExecutorState
	object, ok := value.(map[string]any)
	if !ok {
		return state, errors.New("Invalid executor state")
	}
	phase, ok := object["phase"].(string)
	if !ok || !executorPhases[phase] {
		return state, errors.New("Invalid executor state")
	}
	keeper, ok := jsPid(object["keeperPid"])
	if !ok {
		return state, errors.New("Invalid executor state")
	}
	nativePid, ok := optionalPid(object, "nativePid")
	if !ok {
		return state, errors.New("Invalid executor state")
	}
	guardianPid, ok := optionalPid(object, "guardianPid")
	if !ok {
		return state, errors.New("Invalid executor state")
	}
	nativeURL, ok := optionalEndpoint(object, "nativeUrl")
	if !ok {
		return state, errors.New("Invalid executor state")
	}
	url, ok := optionalEndpoint(object, "url")
	if !ok {
		return state, errors.New("Invalid executor state")
	}
	taskID, ok := optionalTaskID(object)
	if !ok {
		return state, errors.New("Invalid executor state")
	}
	if phase == "ready" && (url == nil || nativeURL == nil || nativePid == nil) {
		return state, errors.New("Invalid executor state")
	}
	state.Phase = phase
	state.KeeperPid = keeper
	state.NativePid = nativePid
	state.GuardianPid = guardianPid
	state.NativeURL = nativeURL
	state.URL = url
	state.TaskID = taskID
	return state, nil
}

var executorPhases = map[string]bool{
	"starting":     true,
	"ready":        true,
	"disconnected": true,
	"retired":      true,
	"exited":       true,
}

// ReadExecutorFile reads one bounded JSON record from a private executor
// directory. These files inherit the selected user's directory ACL, so no
// credential ever reaches a diagnostic log.
func ReadExecutorFile(directory, name string) (any, error) {
	file, err := openExecutorFile(filepath.Join(directory, name))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// A bounded read also handles a concurrent writer growing the file after a
	// stat, exactly as the TS reader does.
	buffer := make([]byte, maxExecutorFileBytes+1)
	read, err := io.ReadFull(file, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if read > maxExecutorFileBytes {
		return nil, errors.New("Executor state is too large")
	}
	var value any
	if err := json.Unmarshal(buffer[:read], &value); err != nil {
		return nil, err
	}
	return value, nil
}

// SaveExecutorFile writes one record atomically: a fsynced temporary file in
// the same directory is renamed over the target, so a reader never observes a
// half-written record. The temporary file is removed again on any failure.
func SaveExecutorFile(directory, name string, value any) error {
	identifier, err := randomUUID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, identifier+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// Only a temporary file this call created is ever removed, and the random
	// name keeps a collision from touching another writer's file.
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(Marshal(value)); err != nil {
		_ = file.Close()
		return err
	}
	// The TS `flush: true` flag means the bytes reach the disk before the
	// rename publishes them.
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := renameExecutorFile(temporary, filepath.Join(directory, name)); err != nil {
		return err
	}
	failed = false
	return nil
}

// SaveExecutorState validates then publishes the durable state record.
func SaveExecutorState(directory string, state ExecutorState) error {
	// Validate the record exactly as a reader would after decoding it, so a
	// zero keeper pid or an endpoint-less ready phase never reaches the disk.
	var decoded any
	if err := json.Unmarshal(Marshal(state), &decoded); err != nil {
		return err
	}
	if _, err := ParseExecutorState(decoded); err != nil {
		return err
	}
	return SaveExecutorFile(directory, executorStateName, state)
}

// RecordCreatedTask records the creation provenance of a task: which executor
// directory owns it. The directory layout itself is the admission proof, so a
// path outside `<private>/executors/<id>` is refused.
func RecordCreatedTask(directory, taskID string) error {
	clean := filepath.Clean(directory)
	executorID := filepath.Base(clean)
	if !taskIDPattern.MatchString(taskID) || !taskIDPattern.MatchString(executorID) ||
		filepath.Base(filepath.Dir(clean)) != "executors" {
		return errors.New("Invalid executor creation provenance")
	}
	task := filepath.Join(filepath.Dir(filepath.Dir(clean)), "tasks", taskID)
	if err := os.MkdirAll(task, 0o700); err != nil {
		return err
	}
	origin := Marshal(map[string]string{"executorId": executorID})
	return writeExclusive(filepath.Join(task, executorOriginName), origin)
}

// ExecutorReference reads the executor identity a created task points back to.
func ExecutorReference(value any) (string, error) {
	if object, ok := value.(map[string]any); ok {
		if id, isText := object["executorId"].(string); isText && taskIDPattern.MatchString(id) {
			return id, nil
		}
	}
	return "", errors.New("Invalid executor reference")
}

// writeExclusive creates a private file that must not already exist.
func writeExclusive(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// jsPid mirrors the TS pid guard: a positive safe integer.
func jsPid(raw any) (int, bool) {
	value, ok := raw.(float64)
	if !ok || !isSafeInteger(value) || value <= 0 {
		return 0, false
	}
	return int(value), true
}

// optionalPid mirrors `value !== undefined && !pid(value)`: an absent key is
// fine, a present key must be a positive safe integer.
func optionalPid(object map[string]any, key string) (*int, bool) {
	raw, present := object[key]
	if !present {
		return nil, true
	}
	pid, ok := jsPid(raw)
	if !ok {
		return nil, false
	}
	return &pid, true
}

// optionalEndpoint mirrors `value !== undefined && !endpoint(value)`.
func optionalEndpoint(object map[string]any, key string) (*string, bool) {
	raw, present := object[key]
	if !present {
		return nil, true
	}
	text, isText := raw.(string)
	if !isText || !loopbackEndpoint(text) {
		return nil, false
	}
	return &text, true
}

func optionalTaskID(object map[string]any) (*string, bool) {
	raw, present := object["taskId"]
	if !present {
		return nil, true
	}
	text, isText := raw.(string)
	if !isText || !taskIDPattern.MatchString(text) {
		return nil, false
	}
	return &text, true
}

// loopbackEndpoint accepts exactly the loopback websocket form the executors
// publish, with a port in the usable range.
func loopbackEndpoint(value string) bool {
	if !endpointPattern.MatchString(value) {
		return false
	}
	port, err := strconv.Atoi(strings.TrimPrefix(value, "ws://127.0.0.1:"))
	return err == nil && port > 0 && port <= 65535
}

// absoluteWorkspace mirrors the TS isAbsolute gate on the executor workspace.
// Recorded divergence: Node's win32 rule also accepts a drive-less rooted path
// such as `\workspace`, which filepath.IsAbs refuses, so that form is accepted
// here too.
func absoluteWorkspace(value string) bool {
	if filepath.IsAbs(value) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	return strings.HasPrefix(value, `\`) || strings.HasPrefix(value, "/")
}

// randomUUID mirrors crypto.randomUUID for the temporary state file name.
func randomUUID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	text := hex.EncodeToString(bytes[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32], nil
}
