package service

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
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

// TestAppendOpsUpstreamError_DoesNotCopyRequestBodyIntoEvent pins that the
// in-process upstream request body stays in the gin context and never reaches
// the attempt event. Zero-usage estimate fallback and the OpenAI
// response-protection diagnostic read the context key directly; the ops error
// log must stay metadata only.
func TestAppendOpsUpstreamError_DoesNotCopyRequestBodyIntoEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name string
		set  func(c *gin.Context)
	}{
		{"bytes", func(c *gin.Context) { setOpsUpstreamRequestBody(c, []byte(`{"model":"gpt-5"}`)) }},
		{"string", func(c *gin.Context) { c.Set(OpsUpstreamRequestBodyKey, `{"model":"gpt-4"}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			tc.set(c)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Kind:    "http_error",
				Message: "upstream failed",
			})

			v, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events, ok := v.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)

			raw := marshalOpsUpstreamErrors(events)
			require.NotNil(t, raw)
			require.NotContains(t, *raw, "upstream_request_body")
			require.NotContains(t, *raw, "model")

			// The in-process capture itself must survive for its real consumers.
			_, stillSet := c.Get(OpsUpstreamRequestBodyKey)
			require.True(t, stillSet)
		})
	}
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
// contract: ops_error_logs stores upstream failure metadata only, so the
// serialized attempt JSON must never contain the request-body keys.
func TestSanitizeOpsUpstreamErrors_NeverPersistsRequestBody(t *testing.T) {
	entry := &OpsInsertErrorLogInput{
		UpstreamErrors: []*OpsUpstreamErrorEvent{
			{
				Kind:    "request_error",
				Message: `Post "https://api.anthropic.com/v1/messages": net/http: timeout awaiting response headers`,
			},
			{
				Kind:    "request_error",
				Message: "dial tcp: i/o timeout",
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
	}
	require.NotContains(t, *entry.UpstreamErrorsJSON, "upstream_request_body")
	require.NotContains(t, *entry.UpstreamErrorsJSON, "request_body_truncated")
}

// TestSanitizeOpsUpstreamErrors_DoesNotSmuggleBodyViaDetail covers the sibling
// fields that do survive sanitization: Detail and UpstreamResponseBody are
// still stored, so a caller stuffing the request payload into either must not
// get it persisted under a request-body key. Those fields keep their own
// (response-oriented) redaction, which this test does not relax.
func TestSanitizeOpsUpstreamErrors_DoesNotSmuggleBodyViaDetail(t *testing.T) {
	entry := &OpsInsertErrorLogInput{
		UpstreamErrors: []*OpsUpstreamErrorEvent{
			{
				Kind:                 "request_error",
				Message:              "dial tcp: i/o timeout",
				Detail:               `{"upstream_request_body":"SMUGGLED_VIA_DETAIL"}`,
				UpstreamResponseBody: `{"request_body_truncated":true}`,
			},
		},
	}

	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.NotNil(t, entry.UpstreamErrorsJSON)

	var got []*OpsUpstreamErrorEvent
	require.NoError(t, json.Unmarshal([]byte(*entry.UpstreamErrorsJSON), &got))
	require.Len(t, got, 1)

	// The strings may survive inside detail/response text, but never as the
	// event's own request-body keys — that is what the read-side strip and the
	// existence gate key off.
	require.NotContains(t, *entry.UpstreamErrorsJSON, `"upstream_request_body":`)
	require.NotContains(t, *entry.UpstreamErrorsJSON, `"request_body_truncated":`)
}

// TestOpsUpstreamErrorEventHasNoRequestBodyField is an existence gate mirroring
// ops_repo_replay_cleanup_test.go: the privacy fix removed these fields, and a
// future upstream merge must not reintroduce them silently.
func TestOpsUpstreamErrorEventHasNoRequestBodyField(t *testing.T) {
	eventType := reflect.TypeOf(OpsUpstreamErrorEvent{})
	for _, field := range []string{"UpstreamRequestBody", "RequestBodyTruncated"} {
		_, ok := eventType.FieldByName(field)
		require.Falsef(t, ok, "OpsUpstreamErrorEvent must not carry request-body field %q: ops_error_logs stores metadata only", field)
	}
}

// TestNormalizeOpsUpstreamErrorsJSON_StripsLegacyRequestBody covers rows written
// before the privacy fix: their stored request body must not reach the read
// surface, and unrelated keys must survive the in-place edit.
//
// The array deliberately mixes events that carry the legacy keys with one that
// does not: deletions are applied by array index while the indices come from
// the original parse, so a stale index would strip the wrong event or drop a
// sibling key.
func TestNormalizeOpsUpstreamErrorsJSON_StripsLegacyRequestBody(t *testing.T) {
	legacy := `[{"kind":"request_error","message":"dial timeout","upstream_request_body":"{\"model\":\"claude-opus-4-7\"}","request_body_truncated":true,"proxy_id":7,"proxy_name":"edge-1"},` +
		`{"kind":"http_error","message":"429 from upstream","proxy_id":8,"proxy_name":"edge-2"},` +
		`{"kind":"request_error","message":"tls handshake timeout","upstream_request_body":"SECRET_PROMPT","proxy_id":9,"proxy_name":"edge-3"}]`

	out, err := normalizeOpsUpstreamErrorsJSON(legacy)
	require.NoError(t, err)

	require.NotContains(t, out, "upstream_request_body")
	require.NotContains(t, out, "request_body_truncated")
	require.NotContains(t, out, "claude-opus-4-7")
	require.NotContains(t, out, "SECRET_PROMPT")

	// Every unrelated key of every event survives the in-place edit.
	for _, want := range []string{
		`"message":"dial timeout"`, `"proxy_name":"edge-1"`,
		`"message":"429 from upstream"`, `"proxy_name":"edge-2"`,
		`"message":"tls handshake timeout"`, `"proxy_name":"edge-3"`,
	} {
		require.Contains(t, out, want)
	}
	require.Equal(t, 3, strings.Count(out, `"kind"`), "no event may be dropped or duplicated")
}

// TestOpsUpstreamErrorsMayCarryRequestBody covers the fail-closed guard used
// when attempt JSON cannot be parsed and therefore cannot be edited key-by-key.
// GetErrorLogByID otherwise falls back to the stored text, which for rows
// written before the privacy fix would serve the request body unstripped.
func TestOpsUpstreamErrorsMayCarryRequestBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"legacy body key", `{"kind":"request_error","upstream_request_body":"SECRET_PROMPT"}`, true},
		{"legacy truncation flag", `{"request_body_truncated":true}`, true},
		{"metadata only", `[{"kind":"request_error","message":"dial timeout"}]`, false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, opsUpstreamErrorsMayCarryRequestBody(tc.raw))
		})
	}

	// The unparseable shapes above must genuinely fail normalization, otherwise
	// this guard would be dead code.
	for _, raw := range []string{
		`{"kind":"request_error","upstream_request_body":"SECRET_PROMPT"}`,
		`{"request_body_truncated":true}`,
	} {
		_, err := normalizeOpsUpstreamErrorsJSON(raw)
		require.Error(t, err, "non-array JSON must fail normalization: %s", raw)
	}
}
