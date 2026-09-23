package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/newo-ether/filo/internal/apierror"
	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
	"github.com/newo-ether/filo/internal/uploads"
)

const (
	maxBodyBytes     = 65536
	maxExcludedTurns = 128
	maxRenameUnits   = 4096
	maxImageReaders  = 4
)

var (
	tokenPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
	uuidPattern  = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// SessionAPI is the bounded session surface the HTTP layer drives. It mirrors
// the TS SessionOperations plus the follower attach lease, which returns its
// own release action instead of a separate detached flag.
type SessionAPI interface {
	List(ctx context.Context, cursor string) (protocol.SessionPage, error)
	Create(ctx context.Context, text, clientID string, settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error)
	Models(ctx context.Context) ([]protocol.Model, error)
	Read(ctx context.Context, id, cursor string, activity bool, excludedTurns []string) (protocol.ConversationPage, error)
	Attach(ctx context.Context, id string) (func(), error)
	Send(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error)
	SetModel(ctx context.Context, id, model string) error
	Stop(ctx context.Context, id, turnID string) error
	UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error
	Rename(ctx context.Context, id, name string) (protocol.Session, error)
	Archive(ctx context.Context, id string) (string, error)
}

// StatusSource is optional; a service without live status reports 501 rather
// than an empty list.
type StatusSource interface {
	Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error)
}

// Shutdowner is optional; its presence is advertised as supportsSafeShutdown.
type Shutdowner interface {
	PrepareShutdown(ctx context.Context) error
}

// ServiceIdentity marks a standalone worker service in /v1/info.
type ServiceIdentity struct {
	SessionMode string `json:"sessionMode"`
	Device      string `json:"device"`
}

// DesktopStatus reports the original desktop bridge state in /v1/info.
type DesktopStatus struct {
	State string  `json:"state"`
	Error *string `json:"error,omitempty"`
}

// ServerOptions configures the bounded Filo HTTP surface.
type ServerOptions struct {
	Usage            func(context.Context) (protocol.AccountUsage, error)
	Uploads          *uploads.Store
	Sessions         SessionAPI
	Token            string
	Events           *Hub
	Payloads         *ConversationPayloads
	OnShutdown       func()
	Identity         *ServiceIdentity
	DesktopStatus    func() DesktopStatus
	Hostname         string
	CreateLogPath    string
	TrustCreateTrace bool
}

// Server answers the bounded Filo wire. Every response is bounded by
// MaxResponseBytes and every read is authenticated before any native access.
type Server struct {
	usage            func(context.Context) (protocol.AccountUsage, error)
	uploads          *uploads.Store
	sessions         SessionAPI
	shutdown         Shutdowner
	events           *Hub
	payloads         *ConversationPayloads
	expected         string
	layer            string
	createLayer      string
	onShutdown       func()
	identity         *ServiceIdentity
	desktopStatus    func() DesktopStatus
	hostname         string
	createLog        *createDiagnostics
	trustCreateTrace bool

	imageMu      sync.Mutex
	imageReaders int
}

// NewServer validates the token and prepares the payload cache the paged views
// share. The token must be 256 bits of hexadecimal, exactly as the TS server
// requires.
func NewServer(options ServerOptions) (*Server, error) {
	if !tokenPattern.MatchString(options.Token) {
		return nil, errors.New("Filo requires a 256-bit hexadecimal token")
	}
	if options.Sessions == nil {
		return nil, errors.New("Filo requires a session surface")
	}
	payloads := options.Payloads
	if payloads == nil {
		payloads = NewConversationPayloads(DefaultPayloadCacheBytes)
	}
	events := options.Events
	if events == nil {
		events = NewHub()
	}
	hostname := options.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	layer := "desktop"
	createLayer := "system"
	if options.Identity != nil && options.Identity.SessionMode == "standalone" {
		layer = "worker"
		createLayer = "helper"
	}
	server := &Server{
		usage:            options.Usage,
		uploads:          options.Uploads,
		sessions:         options.Sessions,
		events:           events,
		payloads:         payloads,
		expected:         "Bearer " + options.Token,
		layer:            layer,
		createLayer:      createLayer,
		onShutdown:       options.OnShutdown,
		identity:         options.Identity,
		desktopStatus:    options.DesktopStatus,
		hostname:         hostname,
		trustCreateTrace: options.TrustCreateTrace,
	}
	if options.CreateLogPath != "" {
		server.createLog = &createDiagnostics{path: options.CreateLogPath}
	}
	if shutdowner, ok := options.Sessions.(Shutdowner); ok {
		server.shutdown = shutdowner
	}
	return server, nil
}

// Layer names the history cursor namespace this server pages in: a standalone
// worker keeps its own cursors, an original desktop service keeps the desktop
// ones.
func (s *Server) Layer() string { return s.layer }

// Events exposes the change hub so a native follower can report through it.
func (s *Server) Events() *Hub { return s.events }

type tracking struct {
	http.ResponseWriter
	wrote bool
}

func (t *tracking) WriteHeader(status int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(status)
}

func (t *tracking) Write(data []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(data)
}

func (t *tracking) Flush() {
	if flusher, ok := t.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (t *tracking) Unwrap() http.ResponseWriter { return t.ResponseWriter }

type errorResponse = apierror.Response

type infoResponse struct {
	SupportsRawUploads bool `json:"supportsRawUploads"`
	protocol.ServiceInfo
	Device               string         `json:"device"`
	SupportsSafeShutdown bool           `json:"supportsSafeShutdown"`
	Desktop              *DesktopStatus `json:"desktop,omitempty"`
}

type modelsResponse struct {
	Models []protocol.Model `json:"models"`
}

type statusesResponse struct {
	Statuses []protocol.SessionStatus `json:"statuses"`
}

type archiveResponse struct {
	Archived bool   `json:"archived"`
	Cwd      string `json:"cwd"`
}

type updatedResponse struct {
	Updated bool `json:"updated"`
}

type stoppedResponse struct {
	Stopped bool `json:"stopped"`
}

type createSessionResponse struct {
	protocol.Session
	TurnID   string `json:"turnId"`
	ClientID string `json:"clientId"`
}

func (s *Server) authorized(value string) bool {
	if len(value) != len(s.expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), []byte(s.expected)) == 1
}

func (s *Server) respond(w *tracking, status int, value any) error {
	if failure, ok := value.(errorResponse); ok {
		value = apierror.New(status, failure.Error, strings.TrimPrefix(s.expected, "Bearer "))
	}
	text, err := EncodeResponse(value)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err = io.WriteString(w, text)
	return err
}

// fail preserves native conflict and upstream status semantics.
func (s *Server) fail(w *tracking, err error) {
	status := http.StatusBadGateway
	var rpc *codex.RpcError
	if errors.As(err, &rpc) {
		if rpc.Code != nil && (*rpc.Code == -32600 || *rpc.Code == -32602) {
			status = http.StatusConflict
		}
	}
	if writeErr := s.respond(w, status, errorResponse{Error: err.Error()}); writeErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"Codex request failed"}`)
	}
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	w := &tracking{ResponseWriter: writer}
	if request.Header.Get("Origin") != "" || !s.authorized(request.Header.Get("Authorization")) {
		_ = s.respond(w, http.StatusUnauthorized, errorResponse{Error: "Unauthorized"})
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/sessions" {
		traceID := ""
		if s.trustCreateTrace {
			traceID = request.Header.Get(createTraceHeader)
		}
		if !createTracePattern.MatchString(traceID) {
			traceID, _ = newCreateTraceID()
		}
		if createTracePattern.MatchString(traceID) {
			request = request.Clone(withCreateTrace(request.Context(), traceID, s.createLog))
			recordCreateStage(request.Context(), s.createLayer+"_gateway_received", nil)
		}
	}
	if err := s.route(w, request); err != nil {
		if w.wrote {
			// The status line is already on the wire, exactly the case the TS
			// server answers by destroying the response.
			panic(http.ErrAbortHandler)
		}
		s.fail(w, err)
	}
}

func (s *Server) route(w *tracking, request *http.Request) error {
	query := request.URL.Query()
	cursor := query.Get("cursor")
	excludedTurns := []string{}
	if s.layer == "worker" {
		if values, ok := query["excludeTurns"]; ok {
			excludedTurns = strings.Split(values[0], ",")
		}
	}
	if len(excludedTurns) > maxExcludedTurns {
		return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Invalid excluded native turns"})
	}
	for _, turn := range excludedTurns {
		if !uuidPattern.MatchString(turn) {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Invalid excluded native turns"})
		}
	}
	path := request.URL.Path
	if path == "/v1/uploads" || strings.HasPrefix(path, "/v1/uploads/") {
		uploads.Handler{Store: s.uploads}.ServeHTTP(w, request)
		return nil
	}
	switch {
	case request.Method == http.MethodGet && path == "/v1/usage":
		if s.usage == nil {
			return s.respond(w, http.StatusServiceUnavailable, errorResponse{Error: "Native usage is unavailable"})
		}
		value, err := s.usage(request.Context())
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, value)
	case request.Method == http.MethodGet && path == "/v1/info":
		return s.info(w)
	case request.Method == http.MethodPost && path == "/v1/shutdown" && s.onShutdown != nil && s.local(request):
		return s.shutdownRequest(w, request)
	case request.Method == http.MethodGet && path == "/v1/sessions/status":
		return s.statuses(w, request, query)
	case request.Method == http.MethodGet && path == "/v1/sessions":
		page, err := s.sessions.List(request.Context(), cursor)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, page)
	case request.Method == http.MethodPost && path == "/v1/sessions":
		input, err := readBody(request)
		if err != nil {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		}
		text, clientID, err := s.messageText(input)
		if err != nil {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		}
		var settings protocol.SessionSettings
		if value, exists := input["settings"]; exists {
			object, ok := value.(map[string]any)
			if !ok {
				return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Expected settings object"})
			}
			settings, err = codex.ParseSettings(object)
			if err != nil {
				return s.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			}
		}
		session, receipt, err := s.sessions.Create(request.Context(), text, clientID, settings)
		if err != nil {
			recordCreateStage(request.Context(), s.createLayer+"_create_failed", err)
			return err
		}
		recordCreateStage(request.Context(), s.createLayer+"_create_completed", nil)
		return s.respond(w, http.StatusCreated, createSessionResponse{
			Session: session, TurnID: receipt.TurnID, ClientID: receipt.ClientID,
		})
	case request.Method == http.MethodGet && path == "/v1/models":
		models, err := s.sessions.Models(request.Context())
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, modelsResponse{Models: models})
	}
	id, action, ok := sessionRoute(path)
	if !ok || !uuidPattern.MatchString(id) {
		return s.respond(w, http.StatusNotFound, errorResponse{Error: "Not found"})
	}
	return s.session(w, request, id, action, cursor, excludedTurns)
}

func (s *Server) info(w *tracking) error {
	info := infoResponse{
		SupportsRawUploads:   s.uploads != nil,
		ServiceInfo:          protocol.CodexServiceInfo,
		Device:               s.hostname,
		SupportsSafeShutdown: s.shutdown != nil,
	}
	if s.identity != nil {
		info.SessionMode = s.identity.SessionMode
		info.Device = s.identity.Device
	}
	if s.desktopStatus != nil {
		value := s.desktopStatus()
		info.Desktop = &value
	}
	return s.respond(w, http.StatusOK, info)
}

func (s *Server) shutdownRequest(w *tracking, request *http.Request) error {
	if s.shutdown != nil {
		if err := s.shutdown.PrepareShutdown(request.Context()); err != nil {
			return err
		}
	}
	if err := s.respond(w, http.StatusOK, stoppedResponse{Stopped: true}); err != nil {
		return err
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	if s.onShutdown != nil {
		s.onShutdown()
	}
	return nil
}

func (s *Server) statuses(w *tracking, request *http.Request, query url.Values) error {
	limit := 12
	if s.layer == "worker" {
		limit = 30
	}
	ids := strings.Split(queryParam(query, "ids"), ",")
	distinct := map[string]bool{}
	invalid := len(ids) == 0 || len(ids) > limit
	for _, id := range ids {
		if distinct[id] || !uuidPattern.MatchString(id) {
			invalid = true
		}
		distinct[id] = true
	}
	if invalid {
		message := "Expected 1-" + strconv.Itoa(limit) + " distinct session UUIDs"
		return s.respond(w, http.StatusBadRequest, errorResponse{Error: message})
	}
	source, ok := s.sessions.(StatusSource)
	if !ok {
		return s.respond(w, http.StatusNotImplemented, errorResponse{Error: "Session status is unavailable"})
	}
	statuses, err := source.Statuses(request.Context(), ids)
	if err != nil {
		return err
	}
	return s.respond(w, http.StatusOK, statusesResponse{Statuses: statuses})
}

func (s *Server) local(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	switch host {
	case "127.0.0.1", "::1", "::ffff:127.0.0.1":
		return true
	}
	local, ok := request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && local != nil && local.String() == request.RemoteAddr
}

func (s *Server) session(w *tracking, request *http.Request, id, action, cursor string,
	excludedTurns []string) error {
	ctx := request.Context()
	query := request.URL.Query()
	switch {
	case request.Method == http.MethodGet && action == "":
		if query.Get("includeMetadata") == "true" {
			page, err := s.payloads.Page(ctx, s.sessions, id, cursor, s.layer, excludedTurns)
			if err != nil {
				return err
			}
			return s.respond(w, http.StatusOK, page)
		}
		page, err := ReadConversationPage(ctx, s.sessions, id, cursor, PageOptions{
			Activity:      query.Get("includeActivity") == "true",
			Layer:         s.layer,
			ExcludedTurns: excludedTurns,
		})
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, page)
	case request.Method == http.MethodGet && action == "topology":
		page, err := s.payloads.Topology(ctx, s.sessions, id, cursor, s.layer)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, page)
	case request.Method == http.MethodGet && action == "payloads":
		requested, err := queryValue(query, "messages")
		if err != nil {
			return CodexRpcError("Invalid message identities", -32602)
		}
		// A non-array value is left to the payload bound, which owns the shared
		// identity refusal.
		requests, _ := payloadRequests(requested)
		value, err := s.payloads.Load(ctx, s.sessions, id, requests, s.layer, false)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, value)
	case request.Method == http.MethodGet && action == "image":
		return s.image(w, request, id)
	case request.Method == http.MethodGet && action == "events":
		view := query.Get("view")
		var source *ConversationPayloads
		if view == "topology" || view == "paged" {
			source = s.payloads
		}
		return s.stream(ctx, w, id, source, view == "paged")
	case request.Method == http.MethodPost && action != "":
		return s.mutate(w, request, id, action)
	}
	return s.respond(w, http.StatusNotFound, errorResponse{Error: "Not found"})
}

// image authorizes exactly one message identity and streams its bounded image.
// The reader budget keeps a slow client from pinning native reads open.
func (s *Server) image(w *tracking, request *http.Request, id string) error {
	if !s.acquireImageReader() {
		return s.respond(w, http.StatusTooManyRequests, errorResponse{Error: "Image readers are busy"})
	}
	defer s.releaseImageReader()
	requested, err := queryValue(request.URL.Query(), "messages")
	if err != nil {
		return CodexRpcError("Invalid image identity", -32602)
	}
	items, isArray := requested.([]any)
	if !isArray || len(items) != 1 {
		return CodexRpcError("Expected one image identity", -32602)
	}
	requests, _ := payloadRequests(requested)
	page, err := s.payloads.Load(request.Context(), s.sessions, id, requests, s.layer, true)
	if err != nil {
		return err
	}
	if request.Context().Err() != nil {
		return nil
	}
	index, err := inlineImageIndex(items[0])
	if err != nil {
		return err
	}
	return ServeConversationImage(w, page.Messages[0], index)
}

// inlineImageIndex reads the optional inline selector from the first request
// object. An absent or null selector keeps the native imageView reference,
// exactly as the TS `imageIndex == null` check does.
func inlineImageIndex(item any) (*int, error) {
	object, ok := item.(map[string]any)
	if !ok {
		return nil, nil
	}
	raw, present := object["imageIndex"]
	if !present || raw == nil {
		return nil, nil
	}
	index, ok := ImageIndex(raw)
	if !ok {
		return nil, unavailableImage()
	}
	return index, nil
}

func (s *Server) mutate(w *tracking, request *http.Request, id, action string) error {
	input, err := readBody(request)
	if err != nil {
		return s.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	}
	ctx := request.Context()
	switch action {
	case "rename":
		name, ok := input["name"].(string)
		if !ok || strings.TrimSpace(name) == "" || toolpreview.UTF16Len(name) > maxRenameUnits {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Expected a task name"})
		}
		session, err := s.sessions.Rename(ctx, id, name)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, session)
	case "archive":
		cwd, err := s.sessions.Archive(ctx, id)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, archiveResponse{Archived: true, Cwd: cwd})
	case "settings":
		settings, err := codex.ParseSettings(input)
		if err != nil {
			return err
		}
		if err := s.sessions.UpdateSettings(ctx, id, settings); err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, updatedResponse{Updated: true})
	case "model":
		model, ok := input["model"].(string)
		if !ok || strings.TrimSpace(model) == "" {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Expected model"})
		}
		if err := s.sessions.SetModel(ctx, id, model); err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, updatedResponse{Updated: true})
	case "stop":
		turnID, ok := input["turnId"].(string)
		if !ok || turnID == "" {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: "Expected turnId"})
		}
		if err := s.sessions.Stop(ctx, id, turnID); err != nil {
			return err
		}
		return s.respond(w, http.StatusOK, stoppedResponse{Stopped: true})
	case "messages":
		text, clientID, err := s.messageText(input)
		if err != nil {
			return s.respond(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		}
		receipt, err := s.sessions.Send(ctx, id, text, clientID)
		if err != nil {
			return err
		}
		return s.respond(w, http.StatusAccepted, receipt)
	}
	return s.respond(w, http.StatusNotFound, errorResponse{Error: "Not found"})
}

func (s *Server) acquireImageReader() bool {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	if s.imageReaders >= maxImageReaders {
		return false
	}
	s.imageReaders++
	return true
}

func (s *Server) releaseImageReader() {
	s.imageMu.Lock()
	s.imageReaders--
	s.imageMu.Unlock()
}

// sessionRoute splits /v1/sessions/<id>[/action]. Any suffix outside the known
// action set fails the route, matching the TS path regex.
func sessionRoute(path string) (id, action string, ok bool) {
	rest, found := strings.CutPrefix(path, "/v1/sessions/")
	if !found || rest == "" {
		return "", "", false
	}
	id, action, _ = strings.Cut(rest, "/")
	if id == "" || strings.Contains(action, "/") {
		return "", "", false
	}
	switch action {
	case "", "messages", "events", "model", "settings", "stop", "topology", "payloads", "image", "rename", "archive":
		return id, action, true
	}
	return "", "", false
}

// queryValue mirrors the TS `searchParams.get(name) ?? 'null'`: an absent
// parameter substitutes null while an empty parameter stays a parse failure.
func queryValue(query url.Values, name string) (any, error) {
	values, ok := query[name]
	if !ok || len(values) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal([]byte(values[0]), &value); err != nil {
		return nil, err
	}
	return value, nil
}

// payloadRequests converts a parsed request list. Only a non-array fails the
// conversion; a malformed entry is kept as an empty request so the payload
// bound still produces the shared refusal.
func payloadRequests(value any) ([]PayloadRequest, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	requests := make([]PayloadRequest, 0, len(items))
	for _, item := range items {
		object, isObject := item.(map[string]any)
		if !isObject {
			requests = append(requests, PayloadRequest{})
			continue
		}
		id, _ := object["id"].(string)
		revision, _ := object["revision"].(string)
		requests = append(requests, PayloadRequest{ID: id, Revision: revision})
	}
	return requests, true
}

func readBody(request *http.Request) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes+1))
	if err != nil {
		return nil, errors.New("Invalid JSON object")
	}
	if len(data) > maxBodyBytes {
		return nil, errors.New("Message exceeds 64 KiB")
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, errors.New("Invalid JSON object")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("Invalid JSON object")
	}
	return object, nil
}

// messageText validates one send payload: text plus the optional folded
// attachments and a UUID client identity. The create and the messages routes
// both use it, so a create accepts exactly the same input a send does.
func (s *Server) messageText(input map[string]any) (string, string, error) {
	text, ok := input["text"].(string)
	if value, exists := input["attachments"]; exists {
		var err error
		text, err = s.attachments(text, value)
		if err != nil {
			return "", "", err
		}
	}
	clientID, hasClient := input["clientId"].(string)
	if !ok || strings.TrimSpace(text) == "" || !hasClient || !uuidPattern.MatchString(clientID) {
		return "", "", errors.New("Expected text and UUID clientId")
	}
	return text, clientID, nil
}

func queryParam(query url.Values, name string) string {
	values, ok := query[name]
	if !ok || len(values) == 0 {
		return ""
	}
	return values[0]
}
