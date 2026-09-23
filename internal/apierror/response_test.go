package apierror

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodesPreserveUnfamiliarNativeDiagnostics(t *testing.T) {
	cases := []struct {
		status       int
		detail, code string
	}{
		{502, "Original desktop owner is unavailable; refresh before continuing", "desktop_unavailable"},
		{409, "thread already has an active writer", "session_busy"},
		{502, "Original desktop snapshot is unavailable", "session_unavailable"},
		{502, "Desktop request timed out or cancelled; do not automatically resend", "request_timeout"},
		{409, "A native operation is still pending", "delivery_unconfirmed"},
		{502, "Native send outcome is unconfirmed", "delivery_unconfirmed"},
		{502, "Filo user helper credentials are unavailable", "helper_unavailable"},
		{502, "Desktop runtime is unavailable", "runtime_unavailable"},
		{409, "Native active turn changed", "turn_changed"},
		{502, "The native image is unavailable", "image_unavailable"},
		{400, "invalid attachment upload", "upload_invalid"},
		{429, "attachment storage limit reached", "upload_unavailable"},
		{413, "Attachment chunk is too large", "content_too_large"},
		{503, "Native usage is unavailable", "usage_unavailable"},
		{502, "Thinking level is unavailable", "settings_unavailable"},
		{401, "Desktop IPC provider returned 401", "authentication_failed"},
		{502, "Provider: 401 unauthorized", "native_error"},
		{502, "Something changed upstream", "native_error"},
		{400, "Expected a task name", "invalid_request"},
		{404, "Not found", "not_found"},
		{501, "Session status is unavailable", "not_supported"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := New(tc.status, tc.detail, "")
			if got.Code != tc.code || got.Message == "" || got.Error != tc.detail {
				t.Fatalf("%+v", got)
			}
		})
	}
}
func TestErrorsAreBoundedUnicodeSafeAndRedactedBeforeTruncation(t *testing.T) {
	token := strings.Repeat("a", 64)
	r := New(502, token+"\x00\r\nBearer other-secret "+strings.Repeat("图", 5000), token)
	if strings.Contains(r.Error, token) || strings.Contains(r.Error, "other-secret") ||
		strings.ContainsAny(r.Error, "\x00\r") || len([]rune(r.Error)) != 2048 {
		t.Fatal("unsafe detail")
	}
	data, err := json.Marshal(r)
	if err != nil || !json.Valid(data) || strings.Contains(string(data), "�") {
		t.Fatal("invalid JSON")
	}
}
