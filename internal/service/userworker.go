package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

const (
	// userWorkerEndpointRefusal is the construction refusal of a worker that is
	// not an authenticated loopback endpoint.
	userWorkerEndpointRefusal = "Filo user worker must be an authenticated loopback endpoint"
	// userWorkerResponseLimit bounds one user-agent response body.
	userWorkerResponseLimit = 1024 * 1024
	// userWorkerUnavailable is the fallback message of a refused user-agent call.
	userWorkerUnavailable = "User agent unavailable"
)

// userWorkerCredentialPattern is the shared hexadecimal credential shape. The TS
// literal carries the case-insensitive flag.
var userWorkerCredentialPattern = regexp.MustCompile(`(?i)^[a-f0-9]{64}$`)

// UserWorkerHistory is the read-only native history behind one user worker. Live
// controls belong to the scoped task sessions, never to this worker.
type UserWorkerHistory interface {
	List(ctx context.Context, cursor string) (protocol.SessionPage, error)
	Read(ctx context.Context, id, cursor string, activity bool, excludedTurns []string) (protocol.ConversationPage, error)
}

// UserWorkerToken resolves the bearer credential of one user worker. A fixed
// credential and a lookup that reads it from the helper installation are both
// tokens, so a worker never holds a stale secret.
type UserWorkerToken interface {
	Credential(ctx context.Context) (string, error)
}

// UserWorkerTokenFunc adapts a credential lookup, which is the TS
// `() => Promise<string>` form.
type UserWorkerTokenFunc func(ctx context.Context) (string, error)

// Credential resolves one credential.
func (token UserWorkerTokenFunc) Credential(ctx context.Context) (string, error) {
	return token(ctx)
}

// userWorkerStaticToken is one fixed credential. Only this shape is validated at
// construction, exactly as the TS string branch is.
type userWorkerStaticToken string

// Credential resolves the fixed credential.
func (token userWorkerStaticToken) Credential(context.Context) (string, error) {
	return string(token), nil
}

// StaticUserWorkerToken wraps one fixed credential.
func StaticUserWorkerToken(value string) UserWorkerToken { return userWorkerStaticToken(value) }

// UserWorkerTimeouts bounds one user-worker request. A read covers listing,
// history and streaming requests, while a mutation deliberately includes cold
// helper discovery.
type UserWorkerTimeouts struct {
	Read     time.Duration
	Mutation time.Duration
}

// DefaultUserWorkerTimeouts equals the TS task request timeouts this worker uses.
var DefaultUserWorkerTimeouts = UserWorkerTimeouts{
	Read:     protocol.DefaultTaskRequestTimeouts.Read,
	Mutation: protocol.DefaultTaskRequestTimeouts.Mutation,
}

// userWorkerHTTPClient is shared by every worker. The TS client asks for
// `redirect: 'error'`, so a redirect is never followed and the redirect response
// itself reaches the status check.
var userWorkerHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// UserWorker is the authenticated loopback client of one Filo user helper. It
// serves the native history plus the peripheral user-context APIs of that
// helper, and it owns the private task transport of one executor directory.
type UserWorker struct {
	url      string
	token    UserWorkerToken
	history  UserWorkerHistory
	timeouts UserWorkerTimeouts
	client   *http.Client
	newTasks *CreatedTaskClient

	mu      sync.Mutex
	closed  bool
	changed func(id string)
}

// NewUserWorker admits one user worker. The endpoint must be an authenticated
// loopback address, and a task directory additionally opens the private task
// transport of that directory.
func NewUserWorker(rawURL string, token UserWorkerToken, history UserWorkerHistory,
	taskDirectory string, timeouts UserWorkerTimeouts) (*UserWorker, error) {
	if fixed, isFixed := token.(userWorkerStaticToken); isFixed &&
		!userWorkerCredentialPattern.MatchString(string(fixed)) {
		return nil, errors.New(userWorkerEndpointRefusal)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New(userWorkerEndpointRefusal)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New(userWorkerEndpointRefusal)
	}
	worker := &UserWorker{url: rawURL, token: token, history: history,
		timeouts: timeouts, client: userWorkerHTTPClient}
	if taskDirectory != "" {
		worker.newTasks = NewCreatedTaskClient(taskDirectory, worker.call, worker.openStream,
			worker.reportChanged)
	}
	return worker, nil
}

// SetChangedHandler installs the change sink of the private task transport.
func (worker *UserWorker) SetChangedHandler(changed func(id string)) {
	worker.mu.Lock()
	worker.changed = changed
	worker.mu.Unlock()
}

// NewTasks returns the private task transport of this worker, which is absent
// when the worker was admitted without a task directory.
func (worker *UserWorker) NewTasks() *CreatedTaskClient { return worker.newTasks }

// reportChanged reports one changed task outside every lock.
func (worker *UserWorker) reportChanged(id string) {
	worker.mu.Lock()
	changed := worker.changed
	worker.mu.Unlock()
	if changed != nil {
		changed(id)
	}
}

// live rejects every operation of a closed worker.
func (worker *UserWorker) live() error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closed {
		return errors.New("User worker is closed")
	}
	return nil
}

// authorization resolves one bearer credential. A missing installation is a
// request failure, never a gateway restart: the lookup is retried on the next
// request, after the closure check, exactly as the TS method orders them.
func (worker *UserWorker) authorization(ctx context.Context) (string, error) {
	credential, err := worker.token.Credential(ctx)
	if err != nil {
		return "", CodexRpcError("Filo user helper is not ready. Repair its installation "+
			"or sign in to the selected Windows account.", -32000)
	}
	if err := worker.live(); err != nil {
		return "", err
	}
	if !userWorkerCredentialPattern.MatchString(credential) {
		return "", CodexRpcError("Filo user helper credentials are unavailable", -32000)
	}
	return "Bearer " + credential, nil
}

// call performs one authenticated user-helper request and returns its decoded
// response as raw JSON, so the peripheral guards keep the runtime shape checks
// the TS client applies to `unknown` values.
func (worker *UserWorker) call(ctx context.Context, path string, input any) (json.RawMessage, error) {
	if err := worker.live(); err != nil {
		return nil, err
	}
	authorization, err := worker.authorization(ctx)
	if err != nil {
		return nil, err
	}
	method, timeout := http.MethodGet, worker.timeouts.Read
	var body io.Reader
	if input != nil {
		encoded, encodeErr := json.Marshal(input)
		if encodeErr != nil {
			return nil, encodeErr
		}
		method, timeout, body = http.MethodPost, worker.timeouts.Mutation, bytes.NewReader(encoded)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, worker.url+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", authorization)
	if path == "/v1/sessions" {
		if trace := createTraceID(ctx); createTracePattern.MatchString(trace) {
			request.Header.Set(createTraceHeader, trace)
		}
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := worker.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, userWorkerResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > userWorkerResponseLimit {
		return nil, errors.New("User-agent response limit")
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		code := float64(-32000)
		if response.StatusCode == http.StatusConflict {
			code = -32600
		}
		return nil, CodexRpcError(userWorkerFailure(value), code)
	}
	return json.RawMessage(data), nil
}

// openStream opens one authenticated task event stream. The resolver deadline
// covers the connect only: the returned response stays readable until the caller
// closes it, so a slow stream is never cut at the connect deadline.
func (worker *UserWorker) openStream(ctx context.Context, path string) (*http.Response, error) {
	if err := worker.live(); err != nil {
		return nil, err
	}
	authorization, err := worker.authorization(ctx)
	if err != nil {
		return nil, err
	}
	connectCtx, cancelConnect := context.WithCancel(ctx)
	request, err := http.NewRequestWithContext(connectCtx, http.MethodGet, worker.url+path, nil)
	if err != nil {
		cancelConnect()
		return nil, err
	}
	request.Header.Set("Authorization", authorization)
	timer := time.AfterFunc(worker.timeouts.Read, cancelConnect)
	response, err := worker.client.Do(request)
	timer.Stop()
	if err != nil {
		cancelConnect()
		return nil, err
	}
	response.Body = &userWorkerStreamBody{ReadCloser: response.Body, release: cancelConnect}
	return response, nil
}

// userWorkerStreamBody releases the connect context when the stream ends.
type userWorkerStreamBody struct {
	io.ReadCloser
	release func()
}

// Close ends the stream and releases its connect context.
func (body *userWorkerStreamBody) Close() error {
	err := body.ReadCloser.Close()
	body.release()
	return err
}

// userWorkerFailure reads the refusal message of one user-agent response. This
// value comes from encoding/json, never from the native transport.
func userWorkerFailure(value any) string {
	fields, ok := value.(map[string]any)
	if !ok {
		return userWorkerUnavailable
	}
	message, ok := fields["error"].(string)
	if !ok {
		return userWorkerUnavailable
	}
	return message
}

// List returns one page of the native task catalog.
func (worker *UserWorker) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	return worker.history.List(ctx, cursor)
}

// Read returns one conversation page. A worker cursor is served by the shared
// pager, so a paged reply keeps its own namespace and never collides with a
// desktop cursor of the same task.
func (worker *UserWorker) Read(ctx context.Context, id, cursor string, activity bool,
	excludedTurns []string) (protocol.ConversationPage, error) {
	if strings.HasPrefix(cursor, "filo.page.worker.") {
		return ReadConversationPage(ctx, worker.history, id, cursor, PageOptions{
			Activity: activity, Layer: "worker", ExcludedTurns: excludedTurns})
	}
	return worker.history.Read(ctx, id, cursor, activity, excludedTurns)
}

// Create asks the helper for one new task, its optional drafted settings and its
// first turn in a single request, so a created session is never empty.
func (worker *UserWorker) Create(ctx context.Context, text, clientID string,
	settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	recordCreateStage(ctx, createStageSystemForwardStarted, nil)
	payload := map[string]any{"text": text, "clientId": clientID}
	if object := createSettingsObject(settings); object != nil {
		payload["settings"] = object
	}
	raw, err := worker.call(ctx, "/v1/sessions", payload)
	if err != nil {
		recordCreateStage(ctx, createStageSystemForwardFailed, err)
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	var session protocol.Session
	if err := json.Unmarshal(raw, &session); err != nil {
		recordCreateStage(ctx, createStageSystemForwardInvalidResponse, err)
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	if !taskIDPattern.MatchString(session.ID) {
		err := errors.New("Invalid user helper creation response")
		recordCreateStage(ctx, createStageSystemForwardInvalidResponse, err)
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	var receipt protocol.SendReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.TurnID == "" || receipt.ClientID != clientID {
		err := errors.New("Invalid user helper creation receipt")
		recordCreateStage(ctx, createStageSystemForwardInvalidResponse, err)
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	recordCreateStage(ctx, createStageSystemForwardCompleted, nil)
	return session, receipt, nil
}

// createSettingsObject renders the drafted settings of a creation as the wire
// object the helper parses, and reports nothing when no field was drafted. A
// null serviceTier keeps its meaning of the native default.
func createSettingsObject(settings protocol.SessionSettings) map[string]any {
	object := map[string]any{}
	if settings.Model.Known {
		object["model"] = settings.Model.Value
	}
	if settings.Effort.Known {
		object["effort"] = settings.Effort.Value
	}
	if settings.ServiceTier.Known {
		if settings.ServiceTier.Value == nil {
			object["serviceTier"] = nil
		} else {
			object["serviceTier"] = *settings.ServiceTier.Value
		}
	}
	if len(object) == 0 {
		return nil
	}
	return object
}

// Rename renames one task through the helper.
func (worker *UserWorker) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	raw, err := worker.call(ctx, "/v1/sessions/"+id+"/rename", map[string]any{"name": name})
	if err != nil {
		return protocol.Session{}, err
	}
	var session protocol.Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return protocol.Session{}, err
	}
	return session, nil
}

// Archive archives one task and returns its directory. An unconfirmed receipt is
// an error, never an assumed archive.
func (worker *UserWorker) Archive(ctx context.Context, id string) (string, error) {
	raw, err := worker.call(ctx, "/v1/sessions/"+id+"/archive", map[string]any{})
	if err != nil {
		return "", err
	}
	var receipt struct {
		Archived bool    `json:"archived"`
		Cwd      *string `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return "", err
	}
	if !receipt.Archived || receipt.Cwd == nil {
		return "", errors.New("Native archive is unconfirmed")
	}
	return *receipt.Cwd, nil
}

// Models returns the models the helper reports.
func (worker *UserWorker) Models(ctx context.Context) ([]protocol.Model, error) {
	raw, err := worker.call(ctx, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	var receipt struct {
		Models []protocol.Model `json:"models"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	return receipt.Models, nil
}

// Close ends this worker and its private task transport. An accepted native task
// keeps running: only this client stops.
func (worker *UserWorker) Close() {
	worker.mu.Lock()
	worker.closed = true
	worker.mu.Unlock()
	if worker.newTasks != nil {
		worker.newTasks.Close()
	}
}

// Usage reads the selected original account through its private user helper.
func (worker *UserWorker) Usage(ctx context.Context) (protocol.AccountUsage, error) {
	raw, err := worker.call(ctx, "/v1/usage", nil)
	if err != nil {
		return protocol.AccountUsage{}, err
	}
	var result protocol.AccountUsage
	if err := json.Unmarshal(raw, &result); err != nil {
		return protocol.AccountUsage{}, err
	}
	return result, nil
}
