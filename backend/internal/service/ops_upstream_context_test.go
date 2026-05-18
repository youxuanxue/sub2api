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

// TestSanitizeOpsUpstreamErrors_TruncatedBodyKeepsKindClean verifies that
// the request_body_truncated marker rides on its own boolean field rather than
// being suffixed onto Kind (which must stay a clean categorical enum so ops
// dashboards/alerts can group "request_error" without :request_body_truncated
// drift based purely on body size).
func TestSanitizeOpsUpstreamErrors_TruncatedBodyKeepsKindClean(t *testing.T) {
	// Build an upstream body large enough to exceed the 10 KB cap applied in
	// sanitizeOpsUpstreamErrors. We embed it inside a valid JSON object so the
	// sanitizer treats it as a JSON payload (matching the production shape).
	largeContent := strings.Repeat("x", 20*1024)
	body, err := json.Marshal(map[string]any{"messages": []map[string]any{{"role": "user", "content": largeContent}}})
	require.NoError(t, err)

	entry := &OpsInsertErrorLogInput{
		UpstreamErrors: []*OpsUpstreamErrorEvent{
			{
				Kind:                "request_error",
				Message:             `Post "https://api.anthropic.com/v1/messages": net/http: timeout awaiting response headers`,
				UpstreamRequestBody: string(body),
			},
		},
	}

	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.NotNil(t, entry.UpstreamErrorsJSON)

	var got []*OpsUpstreamErrorEvent
	require.NoError(t, json.Unmarshal([]byte(*entry.UpstreamErrorsJSON), &got))
	require.Len(t, got, 1)

	require.Equal(t, "request_error", got[0].Kind, "Kind must stay a clean enum value")
	require.True(t, got[0].RequestBodyTruncated, "RequestBodyTruncated must be true when body exceeds storage cap")
	require.NotEmpty(t, got[0].UpstreamRequestBody)
	require.LessOrEqual(t, len(got[0].UpstreamRequestBody), 10*1024)
	require.NotContains(t, *entry.UpstreamErrorsJSON, ":request_body_truncated",
		"the truncated marker must not be suffixed onto kind")
}

// TestSanitizeOpsUpstreamErrors_SmallBodyKeepsTruncatedFalse verifies the
// boolean is false (and omitted from JSON via omitempty) for bodies that fit.
func TestSanitizeOpsUpstreamErrors_SmallBodyKeepsTruncatedFalse(t *testing.T) {
	entry := &OpsInsertErrorLogInput{
		UpstreamErrors: []*OpsUpstreamErrorEvent{
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
	require.Len(t, got, 1)

	require.Equal(t, "request_error", got[0].Kind)
	require.False(t, got[0].RequestBodyTruncated)
	require.NotContains(t, *entry.UpstreamErrorsJSON, "request_body_truncated",
		"omitempty must drop the false flag from serialized JSON")
}
