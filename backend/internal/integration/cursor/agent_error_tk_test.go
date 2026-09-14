package cursor

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAgentConnectRejectionDiagnostics(t *testing.T) {
	for code, status := range map[string]int{"invalid_argument": 400, "resource_exhausted": 429, "unauthenticated": 401, "permission_denied": 403, "unavailable": 502} {
		t.Run(code, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": "invalid tool schema; credential-value Bearer hidden-secret password=secret\n" + strings.Repeat("x", 4096)}})
			require.NoError(t, err)
			header := [5]byte{2}
			binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
			_, err = RunAgent(t.Context(), "credential-value", AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "hello"}}}, func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(append(header[:], raw...)))}, nil
			}, nil)
			var rejection *AgentRejection
			require.True(t, errors.As(err, &rejection))
			var upstream *Error
			require.ErrorAs(t, err, &upstream)
			require.Equal(t, status, upstream.Status)
			require.Equal(t, code, rejection.Code)
			require.NotEmpty(t, rejection.RequestID)
			require.Contains(t, rejection.Diagnostic, "invalid tool schema")
			require.NotContains(t, rejection.Diagnostic, "credential-value")
			require.NotContains(t, rejection.Diagnostic, "hidden-secret")
			require.NotContains(t, rejection.Diagnostic, "password=secret")
			require.NotContains(t, rejection.Diagnostic, "\n")
			require.LessOrEqual(t, len(rejection.Diagnostic), 2051)
			require.NotContains(t, err.Error(), "invalid tool schema")
		})
	}
}

func TestAgentConnectStructuredDetails(t *testing.T) {
	// Literal protobuf: ErrorDetails.error=41, CustomErrorDetails title/detail.
	nested := []byte{0x0a, 5, 'Q', 'u', 'o', 't', 'a', 0x12, 13, 't', 'o', 'k', 'e', 'n', '=', 's', 'e', 'c', 'r', 'e', 't', '!', 0x20, 0}
	raw := append([]byte{0x08, 41, 0x12, byte(len(nested))}, nested...)
	rejection := newAgentRejection(429, "resource_exhausted", "Error", "", "request", agentConnectDetail{Type: "aiserver.v1.ErrorDetails", Value: base64.StdEncoding.EncodeToString(raw)})
	require.Contains(t, rejection.Diagnostic, "supplier_error=41")
	require.Contains(t, rejection.Diagnostic, "title=Quota")
	require.Contains(t, rejection.Diagnostic, "retryable=false")
	require.NotContains(t, rejection.Diagnostic, "secret")
	require.NotContains(t, rejection.Error(), "Quota")
	for _, detail := range []agentConnectDetail{{Type: "unknown", Value: base64.StdEncoding.EncodeToString(raw)}, {Type: "aiserver.v1.ErrorDetails", Value: "%%%"}, {Type: "aiserver.v1.ErrorDetails", Value: base64.StdEncoding.EncodeToString([]byte{0xff})}} {
		require.Empty(t, agentErrorDetailsText([]agentConnectDetail{detail}, ""))
	}
}

func TestMessagesFailureRetainsCorrelation(t *testing.T) {
	raw := []byte(`{"error":{"code":"invalid_argument","message":"invalid fixture"}}`)
	header := [5]byte{2}
	binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
	resp, err := Messages(t.Context(), "test-token", []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"hello"}]}`), nil, "composer-2.5", func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(append(header[:], raw...)))}, nil
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 400, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("X-Request-Id"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"type":"invalid_request_error"`)
	require.NotContains(t, string(body), "invalid fixture")
}

func TestAgentExpandedDiagnosticsRedactMetadataAndAdditionalInfo(t *testing.T) {
	const token = "private-oauth-fixture-token"
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendString(entry, "provider_response")
	entry = protowire.AppendTag(entry, 2, protowire.BytesType)
	entry = protowire.AppendString(entry, "invalid tool history "+token)
	custom := protowire.AppendTag(nil, 7, protowire.BytesType)
	custom = protowire.AppendBytes(custom, entry)
	raw := protowire.AppendTag(nil, 2, protowire.BytesType)
	raw = protowire.AppendBytes(raw, custom)
	rejection := newAgentRejection(400, "invalid_argument", "Error", token, "fixture-id",
		agentConnectDetail{Type: "aiserver.v1.ErrorDetails", Value: base64.StdEncoding.EncodeToString(raw)},
		agentConnectDetail{Type: "unknown.detail", Value: base64.StdEncoding.EncodeToString([]byte(token))})
	require.Empty(t, rejection.ProviderMessage)
	require.Empty(t, rejection.ActionRequired)
	require.Contains(t, rejection.Diagnostic, "invalid tool history")
	require.NotContains(t, rejection.Diagnostic, token)
	require.Contains(t, rejection.DetailInventory, "unknown.detail")
	require.NotContains(t, rejection.DetailInventory, base64.StdEncoding.EncodeToString([]byte(token)))
	require.NotContains(t, rejection.Error(), "invalid tool history")
	metadata := agentDiagnosticJSON(map[string]any{"authorization": []string{"Bearer hidden"}, "reason": []string{strings.Repeat("x", 2040) + token}}, token, 2048)
	require.NotContains(t, metadata, "hidden")
	require.NotContains(t, metadata, "private-oauth")
	require.LessOrEqual(t, len(metadata), 2051)
}

func TestAgentPolicyReviewDiagnosticIsRetainedAndRedacted(t *testing.T) {
	// Captured supplier 13 carries an explicit action, not a tool-validation field.
	const detail = "CA0SlgEKHFJlcXVlc3QgYmxvY2tlZCBieSBBbnRocm9waWMSXVdlIGFyZSB1bmFibGUgdG8gY29tcGxldGUgdGhpcyByZXF1ZXN0IGJlY2F1c2UgaXQgd2FzIGJsb2NrZWQgdW5kZXIgQW50aHJvcGljJ3MgVXNhZ2UgUG9saWN5LiAAUhUKE2N5YmVyX3BvbGljeV9yZXZpZXcYAQ"
	err := newAgentRejection(400, "invalid_argument", "Error", "", "fixture", agentConnectDetail{Type: "aiserver.v1.ErrorDetails", Value: detail})
	require.Equal(t, "cyber_policy_review", err.ActionRequired)
	require.Contains(t, err.ProviderMessage, "Usage Policy")
	require.Contains(t, err.Diagnostic, "supplier_error=13")
	require.Contains(t, err.Diagnostic, "action_required=cyber_policy_review")
	require.NotContains(t, err.Error(), "cyber_policy_review", "operator metadata must not leak through the public error")
	const token = "private-action-fixture-token"
	analytics := protowire.AppendTag(nil, 1, protowire.BytesType)
	analytics = protowire.AppendString(analytics, "review-"+token)
	custom := protowire.AppendTag(nil, 10, protowire.BytesType)
	custom = protowire.AppendBytes(custom, analytics)
	raw := protowire.AppendTag(nil, 2, protowire.BytesType)
	raw = protowire.AppendBytes(raw, custom)
	rejected := newAgentRejection(400, "invalid_argument", "Error", token, "fixture", agentConnectDetail{Type: "aiserver.v1.ErrorDetails", Value: base64.StdEncoding.EncodeToString(raw)})
	require.Contains(t, rejected.Diagnostic, "action_required=review-")
	require.NotContains(t, rejected.ActionRequired, token)
	require.NotContains(t, rejected.Diagnostic, token)
	require.Empty(t, agentAnalyticsActionRequired([]byte{0xff}))
}
