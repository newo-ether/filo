package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/apierror"
)

const (
	createTraceHeader   = "X-Filo-Create-Trace"
	createSystemLogName = "create-system.jsonl"
	createHelperLogName = "create-helper.jsonl"

	createStageSystemGatewayReceived        = "system_gateway_received"
	createStageSystemForwardStarted         = "system_forward_started"
	createStageSystemForwardFailed          = "system_forward_failed"
	createStageSystemForwardInvalidResponse = "system_forward_invalid_response"
	createStageSystemForwardCompleted       = "system_forward_completed"
	createStageSystemCreateFailed           = "system_create_failed"
	createStageSystemCreateCompleted        = "system_create_completed"
	createStageHelperGatewayReceived        = "helper_gateway_received"
	createStageHelperAllocationStarted      = "helper_executor_allocation_started"
	createStageHelperAllocationFailed       = "helper_executor_allocation_failed"
	createStageHelperAllocationCompleted    = "helper_executor_allocation_completed"
	createStageHelperNativeStartStarted     = "helper_native_start_started"
	createStageHelperNativeStartFailed      = "helper_native_start_failed"
	createStageHelperProvenanceStarted      = "helper_provenance_verification_started"
	createStageHelperProvenanceFailed       = "helper_provenance_verification_failed"
	createStageHelperProvenanceVerified     = "helper_provenance_verified"
	createStageHelperCreateFailed           = "helper_create_failed"
	createStageHelperCreateCompleted        = "helper_create_completed"
	createStageExecutorNativeAcknowledged   = "executor_native_acknowledged"
	createStageExecutorLifecycleFailed      = "executor_lifecycle_state_failed"
	createStageExecutorProvenanceFailed     = "executor_provenance_record_failed"
	createStageExecutorProvenanceRecorded   = "executor_provenance_recorded"
	createStageExecutorGuardianFailed       = "executor_guardian_bind_failed"
	createStageExecutorGuardianBound        = "executor_guardian_bound"
)

var createTracePattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

var createDiagnosticStages = map[string]bool{
	createStageSystemGatewayReceived:        true,
	createStageSystemForwardStarted:         true,
	createStageSystemForwardFailed:          true,
	createStageSystemForwardInvalidResponse: true,
	createStageSystemForwardCompleted:       true,
	createStageSystemCreateFailed:           true,
	createStageSystemCreateCompleted:        true,
	createStageHelperGatewayReceived:        true,
	createStageHelperAllocationStarted:      true,
	createStageHelperAllocationFailed:       true,
	createStageHelperAllocationCompleted:    true,
	createStageHelperNativeStartStarted:     true,
	createStageHelperNativeStartFailed:      true,
	createStageHelperProvenanceStarted:      true,
	createStageHelperProvenanceFailed:       true,
	createStageHelperProvenanceVerified:     true,
	createStageHelperCreateFailed:           true,
	createStageHelperCreateCompleted:        true,
	createStageExecutorNativeAcknowledged:   true,
	createStageExecutorLifecycleFailed:      true,
	createStageExecutorProvenanceFailed:     true,
	createStageExecutorProvenanceRecorded:   true,
	createStageExecutorGuardianFailed:       true,
	createStageExecutorGuardianBound:        true,
}

type createTraceKey struct{}

type createTrace struct {
	id          string
	diagnostics *createDiagnostics
}

type createDiagnostics struct {
	path string
	mu   sync.Mutex
}

type createDiagnosticEvent struct {
	Time      string `json:"time"`
	Trace     string `json:"trace"`
	Stage     string `json:"stage"`
	ErrorCode string `json:"errorCode,omitempty"`
}

func newCreateTraceID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func withCreateTrace(ctx context.Context, id string, diagnostics *createDiagnostics) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !createTracePattern.MatchString(id) {
		return ctx
	}
	return context.WithValue(ctx, createTraceKey{}, createTrace{id: id, diagnostics: diagnostics})
}

func createTraceFrom(ctx context.Context) (createTrace, bool) {
	if ctx == nil {
		return createTrace{}, false
	}
	trace, ok := ctx.Value(createTraceKey{}).(createTrace)
	return trace, ok && createTracePattern.MatchString(trace.id)
}

func createTraceID(ctx context.Context) string {
	trace, _ := createTraceFrom(ctx)
	return trace.id
}

func recordCreateStage(ctx context.Context, stage string, err error) {
	trace, ok := createTraceFrom(ctx)
	if !ok || trace.diagnostics == nil {
		return
	}
	trace.diagnostics.record(trace.id, stage, createErrorCode(err))
}

func createErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return apierror.New(http.StatusBadGateway, err.Error(), "").Code
}

func (d *createDiagnostics) record(trace, stage, code string) {
	if d == nil || !filepath.IsAbs(d.path) || !createTracePattern.MatchString(trace) ||
		!createDiagnosticStages[stage] {
		return
	}
	event := createDiagnosticEvent{
		Time:      time.Now().UTC().Format(time.RFC3339Nano),
		Trace:     trace,
		Stage:     stage,
		ErrorCode: code,
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	file, err := os.OpenFile(d.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(line, '\n'))
	_ = file.Close()
}

func executorCreateDiagnostics(directory, trace string) *createDiagnostics {
	if !createTracePattern.MatchString(trace) {
		return nil
	}
	executors := filepath.Dir(filepath.Clean(directory))
	root := filepath.Dir(executors)
	if filepath.Base(executors) != "executors" || filepath.Base(root) != serviceTaskDirectoryName {
		return nil
	}
	return &createDiagnostics{path: filepath.Join(filepath.Dir(root), createHelperLogName)}
}
