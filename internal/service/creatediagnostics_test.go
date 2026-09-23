package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func readCreateDiagnosticEvents(t *testing.T, path string) []createDiagnosticEvent {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open create diagnostics: %v", err)
	}
	defer file.Close()
	var events []createDiagnosticEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var fields map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &fields); err != nil {
			t.Fatalf("decode create diagnostic fields: %v", err)
		}
		for key := range fields {
			switch key {
			case "time", "trace", "stage", "errorCode":
			default:
				t.Fatalf("unexpected create diagnostic field %q", key)
			}
		}
		if len(fields) < 3 || len(fields) > 4 {
			t.Fatalf("create diagnostic fields = %v", fields)
		}
		var event createDiagnosticEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode create diagnostic event: %v", err)
		}
		if event.Time == "" || !createTracePattern.MatchString(event.Trace) ||
			!createDiagnosticStages[event.Stage] {
			t.Fatalf("invalid create diagnostic event = %+v", event)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan create diagnostics: %v", err)
	}
	return events
}

func createDiagnosticStagesOf(events []createDiagnosticEvent) []string {
	stages := make([]string, 0, len(events))
	for _, event := range events {
		stages = append(stages, event.Stage)
	}
	return stages
}

func TestCreateDiagnosticsAuthenticateAndTrustOnlyPrivateTraces(t *testing.T) {
	publicLog := filepath.Join(t.TempDir(), createSystemLogName)
	publicSessions := newFakeSessions(0)
	public := newTestServer(t, ServerOptions{
		Sessions:      publicSessions,
		CreateLogPath: publicLog,
	})
	forged := strings.Repeat("f", 32)
	unauthorized := post(t, public.URL+"/v1/sessions", "private prompt", map[string]string{
		createTraceHeader: forged,
	})
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()
	if _, _, mutations := publicSessions.counts(); mutations != 0 {
		t.Fatalf("unauthorized create reached %d mutations", mutations)
	}
	if _, err := os.Stat(publicLog); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unauthorized create diagnostics exist: %v", err)
	}

	headers := authHeaders()
	headers[createTraceHeader] = forged
	created := post(t, public.URL+"/v1/sessions", `{"text":"private prompt","clientId":"`+payloadSession+`"}`, headers)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("public create status = %d", created.StatusCode)
	}
	created.Body.Close()
	publicTraces := publicSessions.createTraceValues()
	if len(publicTraces) != 1 || !createTracePattern.MatchString(publicTraces[0]) ||
		publicTraces[0] == forged {
		t.Fatalf("public create traces = %v", publicTraces)
	}
	publicEvents := readCreateDiagnosticEvents(t, publicLog)
	if stages := createDiagnosticStagesOf(publicEvents); !reflect.DeepEqual(stages, []string{
		createStageSystemGatewayReceived,
		createStageSystemCreateCompleted,
	}) {
		t.Fatalf("public create stages = %v", stages)
	}
	for _, event := range publicEvents {
		if event.Trace != publicTraces[0] || event.ErrorCode != "" {
			t.Fatalf("public create event = %+v", event)
		}
	}

	helperLog := filepath.Join(t.TempDir(), createHelperLogName)
	helperSessions := newFakeSessions(0)
	helper := newTestServer(t, ServerOptions{
		Sessions:         helperSessions,
		Identity:         &ServiceIdentity{SessionMode: "standalone", Device: "fixture"},
		CreateLogPath:    helperLog,
		TrustCreateTrace: true,
	})
	inherited := strings.Repeat("b", 32)
	for _, trace := range []string{inherited, strings.Repeat("C", 32), strings.Repeat("d", 31)} {
		headers := authHeaders()
		headers[createTraceHeader] = trace
		response := post(t, helper.URL+"/v1/sessions", `{"text":"hello","clientId":"`+payloadSession+`"}`, headers)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("helper create status = %d", response.StatusCode)
		}
		response.Body.Close()
	}
	helperTraces := helperSessions.createTraceValues()
	if len(helperTraces) != 3 || helperTraces[0] != inherited {
		t.Fatalf("helper create traces = %v", helperTraces)
	}
	for index := 1; index < len(helperTraces); index++ {
		if !createTracePattern.MatchString(helperTraces[index]) || helperTraces[index] == inherited {
			t.Fatalf("generated helper trace %d = %q", index, helperTraces[index])
		}
	}
}

func TestCreateDiagnosticsForwardTheSameTraceToTheHelper(t *testing.T) {
	helperLog := filepath.Join(t.TempDir(), createHelperLogName)
	helperSessions := newFakeSessions(0)
	helper := newTestServer(t, ServerOptions{
		Sessions:         helperSessions,
		Identity:         &ServiceIdentity{SessionMode: "standalone", Device: "fixture"},
		CreateLogPath:    helperLog,
		TrustCreateTrace: true,
	})
	worker := newUserWorkerFixture(t, helper.URL, StaticUserWorkerToken(testToken),
		userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
	systemLog := filepath.Join(t.TempDir(), createSystemLogName)
	trace := strings.Repeat("a", 32)
	ctx := withCreateTrace(context.Background(), trace, &createDiagnostics{path: systemLog})
	if _, _, err := worker.Create(ctx, "hello", payloadSession, protocol.SessionSettings{}); err != nil {
		t.Fatalf("forward create: %v", err)
	}
	if traces := helperSessions.createTraceValues(); !reflect.DeepEqual(traces, []string{trace}) {
		t.Fatalf("helper traces = %v", traces)
	}
	for _, path := range []string{systemLog, helperLog} {
		for _, event := range readCreateDiagnosticEvents(t, path) {
			if event.Trace != trace {
				t.Fatalf("trace in %s = %q", filepath.Base(path), event.Trace)
			}
		}
	}
	if stages := createDiagnosticStagesOf(readCreateDiagnosticEvents(t, systemLog)); !reflect.DeepEqual(stages, []string{
		createStageSystemForwardStarted,
		createStageSystemForwardCompleted,
	}) {
		t.Fatalf("system forward stages = %v", stages)
	}
}

func TestCreateDiagnosticsKeepErrorsBoundedAndLoggingBestEffort(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), createSystemLogName)
	failing := newFakeSessions(0)
	failing.createErr = errors.New("context deadline exceeded: private prompt at C:\\secret")
	server := newTestServer(t, ServerOptions{Sessions: failing, CreateLogPath: logPath})
	response := post(t, server.URL+"/v1/sessions", `{"text":"private prompt","clientId":"`+payloadSession+`"}`, authHeaders())
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed create status = %d", response.StatusCode)
	}
	response.Body.Close()
	events := readCreateDiagnosticEvents(t, logPath)
	if len(events) != 2 || events[1].Stage != createStageSystemCreateFailed ||
		events[1].ErrorCode != "request_timeout" {
		t.Fatalf("failed create events = %+v", events)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read create log: %v", err)
	}
	for _, secret := range []string{testToken, "private prompt", `C:\secret`, failing.createErr.Error()} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("create diagnostics contain %q", secret)
		}
	}

	working := newFakeSessions(0)
	unwritable := filepath.Join(t.TempDir(), "missing", createSystemLogName)
	bestEffort := newTestServer(t, ServerOptions{Sessions: working, CreateLogPath: unwritable})
	created := post(t, bestEffort.URL+"/v1/sessions", `{"text":"hello","clientId":"`+payloadSession+`"}`, authHeaders())
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("best effort create status = %d", created.StatusCode)
	}
	created.Body.Close()
	if _, _, mutations := working.counts(); mutations != 1 {
		t.Fatalf("best effort create mutations = %d", mutations)
	}
	if _, err := os.Stat(unwritable); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unwritable create log exists: %v", err)
	}
}

func TestScopedCreationRequiresConfirmedProvenance(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	fixture.factory.setOwns(false, nil)
	logPath := filepath.Join(t.TempDir(), createHelperLogName)
	trace := strings.Repeat("d", 32)
	ctx := withCreateTrace(context.Background(), trace, &createDiagnostics{path: logPath})
	if _, _, err := fixture.sessions.Create(ctx, "hello", payloadSession, protocol.SessionSettings{}); err == nil ||
		!strings.Contains(err.Error(), "provenance is unconfirmed") {
		t.Fatalf("create error = %v", err)
	}
	if starts := fixture.peer.callCount("thread/start"); starts != 1 {
		t.Fatalf("thread starts = %d", starts)
	}
	want := []string{
		createStageHelperAllocationStarted,
		createStageHelperAllocationCompleted,
		createStageHelperNativeStartStarted,
		createStageHelperProvenanceStarted,
		createStageHelperProvenanceFailed,
	}
	events := readCreateDiagnosticEvents(t, logPath)
	if stages := createDiagnosticStagesOf(events); !reflect.DeepEqual(stages, want) {
		t.Fatalf("provenance failure stages = %v, want %v", stages, want)
	}
	if events[len(events)-1].ErrorCode != "delivery_unconfirmed" {
		t.Fatalf("provenance error code = %q", events[len(events)-1].ErrorCode)
	}
}
