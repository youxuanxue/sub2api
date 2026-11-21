package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSafeUpstreamURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips query", "https://api.anthropic.com/v1/messages?beta=true", "https://api.anthropic.com/v1/messages"},
		{"strips fragment", "https://api.openai.com/v1/responses#frag", "https://api.openai.com/v1/responses"},
		{"strips both", "https://host/path?token=secret#x", "https://host/path"},
		{"no query or fragment", "https://host/path", "https://host/path"},
		{"empty string", "", ""},
		{"whitespace only", "  ", ""},
		{"query before fragment", "https://h/p?a=1#f", "https://h/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeUpstreamURL(tt.input))
		})
	}
}

func TestAppendOpsUpstreamError_UsesRequestBodyBytesFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	setOpsUpstreamRequestBody(c, []byte(`{"model":"gpt-5"}`))
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:    "http_error",
		Message: "upstream failed",
	})

	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, `{"model":"gpt-5"}`, events[0].UpstreamRequestBody)
}

func TestAppendOpsUpstreamError_UsesRequestBodyStringFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Set(OpsUpstreamRequestBodyKey, `{"model":"gpt-4"}`)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:    "request_error",
		Message: "dial timeout",
	})

	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, `{"model":"gpt-4"}`, events[0].UpstreamRequestBody)
}

func TestAppendOpsUpstreamError_UsesTLSFingerprintProfileFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Set(OpsTLSFingerprintProfileIDKey, int64(42))
	c.Set(OpsTLSFingerprintProfileNameKey, "claude_cli_nodejs25_observed_candidate")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:    "request_error",
		Message: "tls handshake failed",
	})

	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, int64(42), events[0].TLSFingerprintProfileID)
	require.Equal(t, "claude_cli_nodejs25_observed_candidate", events[0].TLSFingerprintProfileName)

	raw := marshalOpsUpstreamErrors(events)
	require.NotNil(t, raw)
	require.Contains(t, *raw, `"tls_fingerprint_profile_id":42`)
	require.Contains(t, *raw, `"tls_fingerprint_profile_name":"claude_cli_nodejs25_observed_candidate"`)
}

// TestSanitizeOpsUpstreamErrors_NeverPersistsRequestBody pins the privacy
// contract: ops_error_logs stores upstream failure metadata only. Request
// payloads are never persisted here regardless of size, so the only place a
// prompt can be recovered for troubleshooting is the QA capture of users who
// left QA capture enabled.
func TestSanitizeOpsUpstreamErrors_NeverPersistsRequestBody(t *testing.T) {
	largeContent := strings.Repeat("x", 20*1024)
	largeBody, err := json.Marshal(map[string]any{"messages": []map[string]any{{"role": "user", "content": largeContent}}})
	require.NoError(t, err)

	entry := &OpsInsertErrorLogInput{
		UpstreamErrors: []*OpsUpstreamErrorEvent{
			{
				Kind:                "request_error",
				Message:             `Post "https://api.anthropic.com/v1/messages": net/http: timeout awaiting response headers`,
				UpstreamRequestBody: string(largeBody),
			},
			{
				Kind:                "request_error",
				Message:             "dial tcp: i/o timeout",
				UpstreamRequestBody: `{"model":"claude-opus-4-7","stream":true}`,
			},
		},
	}

	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.NotNil(t, entry.UpstreamErrorsJSON)

	var got []*OpsUpstreamErrorEvent
	require.NoError(t, json.Unmarshal([]byte(*entry.UpstreamErrorsJSON), &got))
	require.Len(t, got, 2)

	for i, ev := range got {
		require.Equal(t, "request_error", ev.Kind, "Kind must stay a clean enum value (event %d)", i)
		require.Empty(t, ev.UpstreamRequestBody, "request body must never be persisted (event %d)", i)
		require.False(t, ev.RequestBodyTruncated, "truncation flag is meaningless once bodies are dropped (event %d)", i)
	}
	require.NotContains(t, *entry.UpstreamErrorsJSON, "upstream_request_body")
	require.NotContains(t, *entry.UpstreamErrorsJSON, "request_body_truncated")
	require.NotContains(t, *entry.UpstreamErrorsJSON, largeContent[:64])
}

// TestSanitizeOpsUpstreamErrors_DropsEventWithOnlyRequestBody verifies an
// attempt whose only payload was the request body is dropped rather than
// stored as an empty row.
func TestSanitizeOpsUpstreamErrors_DropsEventWithOnlyRequestBody(t *testing.T) {
	entry := &OpsInsertErrorLogInput{
		UpstreamErrors: []*OpsUpstreamErrorEvent{
			{UpstreamRequestBody: `{"model":"claude-opus-4-7"}`},
		},
	}

	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.Nil(t, entry.UpstreamErrorsJSON, "an attempt carrying only a request body leaves nothing to store")
}
