// Package apierror defines bounded public errors. Clients translate Code;
// Message is the English fallback. Codes never authorize a mutation retry.
package apierror

import (
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

type Response struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

var messages = map[string]string{
	"authentication_failed": "Filo rejected the access token. Use the token from the target computer.",
	"invalid_request":       "Filo could not read this request. Check the supplied values.",
	"not_found":             "The requested item is no longer available. Refresh the list.",
	"not_supported":         "This operation is unavailable in the connected client.",
	"session_busy":          "Another Codex process owns this conversation. Wait for it to release the conversation.",
	"desktop_unavailable":   "Filo cannot reach Codex Desktop. Open Codex on the target computer.",
	"session_unavailable":   "Codex has not made this conversation ready. Check it on the target computer.",
	"helper_unavailable":    "Filo cannot reach its user helper. Check the Filo service on the target computer.",
	"runtime_unavailable":   "The Codex Desktop runtime is unavailable. Open Codex to finish its setup.",
	"service_restarting":    "Filo is shutting down. Wait for the service to become available.",
	"request_timeout":       "The request timed out. Check the conversation before sending again.",
	"delivery_unconfirmed":  "Codex has not confirmed the operation. Check the conversation before repeating it.",
	"turn_changed":          "The active response changed. Refresh the conversation before trying the operation again.",
	"image_unavailable":     "This image is unavailable on the target computer.",
	"upload_invalid":        "The attachment upload is incomplete or invalid. Select the file again.",
	"upload_unavailable":    "Filo cannot store the attachment. Check storage on the target computer.",
	"busy":                  "Filo is handling too many requests. Wait a moment.",
	"content_too_large":     "The response or attachment exceeds the supported size.",
	"usage_unavailable":     "Codex usage information is currently unavailable.",
	"settings_unavailable":  "Codex does not support the selected setting for this model.",
	"native_error":          "Codex could not complete the request. See the error details.",
}

type rule struct{ prefix, code string }

// IPC and existing private worker failures are untyped. Match known Filo-owned
// prefixes only; unfamiliar provider errors retain their original diagnostics.
var rules = []rule{
	{"Original desktop owner", "desktop_unavailable"},
	{"Original desktop is unavailable", "desktop_unavailable"},
	{"Original Codex desktop is unavailable", "desktop_unavailable"},
	{"Desktop IPC", "desktop_unavailable"},
	{"Desktop pipe", "desktop_unavailable"},
	{"Original desktop snapshot is unavailable", "session_unavailable"},
	{"Native task is not ready", "session_unavailable"},
	{"Filo user helper is shutting down", "service_restarting"},
	{"User agent unavailable", "helper_unavailable"},
	{"User worker is closed", "helper_unavailable"},
	{"Filo user helper credentials", "helper_unavailable"},
	{"Native task lifetime protection is unavailable", "helper_unavailable"},
	{"Native executor keeper is unavailable", "helper_unavailable"},
	{"Filo native executor is unavailable", "helper_unavailable"},
	{"Desktop runtime is unavailable", "runtime_unavailable"},
	{"Desktop request timed out", "request_timeout"},
	{"context deadline exceeded", "request_timeout"},
	{"Native steer outcome is unconfirmed", "delivery_unconfirmed"},
	{"Native send outcome is unconfirmed", "delivery_unconfirmed"},
	{"Native creation provenance is unconfirmed", "delivery_unconfirmed"},
	{"A native operation is still pending", "delivery_unconfirmed"},
	{"Native active turn changed", "turn_changed"},
	{"The native image is unavailable", "image_unavailable"},
	{"invalid attachment upload", "upload_invalid"},
	{"attachment storage limit reached", "upload_unavailable"},
	{"Attachment storage is unavailable", "upload_unavailable"},
	{"Attachment chunk is too large", "content_too_large"},
	{"Image readers are busy", "busy"},
	{"Native usage is unavailable", "usage_unavailable"},
	{"Thinking level is unavailable", "settings_unavailable"},
	{"Service tier is unavailable", "settings_unavailable"},
}

var bearer = regexp.MustCompile(`(?i)bearer[ \t]+[a-z0-9._~+/=-]+`)

func clean(detail, secret string) string {
	if secret != "" {
		detail = strings.ReplaceAll(detail, secret, "[redacted]")
	}
	detail = bearer.ReplaceAllString(detail, "Bearer [redacted]")
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, detail)
	runes := []rune(strings.TrimSpace(detail))
	if len(runes) > 2048 {
		runes = runes[:2048]
	}
	return string(runes)
}

func New(status int, detail, secret string) Response {
	code := "native_error"
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = "authentication_failed"
	case http.StatusBadRequest:
		code = "invalid_request"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusNotImplemented:
		code = "not_supported"
	case http.StatusRequestEntityTooLarge:
		code = "content_too_large"
	case http.StatusTooManyRequests:
		code = "busy"
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		code = "request_timeout"
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		for _, r := range rules {
			if strings.HasPrefix(detail, r.prefix) {
				code = r.code
				break
			}
		}
		if strings.Contains(detail, "already has an active writer") {
			code = "session_busy"
		}
	}
	return Response{Code: code, Message: messages[code], Error: clean(detail, secret)}
}
