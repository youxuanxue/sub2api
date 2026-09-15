package cursor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
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
	require.Equal(t, "Error", rejection.ProviderMessage)
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

func TestAgentTransportErrorPreservesCauseWithoutExposingCredential(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		t.Run(cause.Error(), func(t *testing.T) {
			_, err := RunAgent(t.Context(), "private-token", AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "hello"}}}, func(*http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Post", URL: "https://private-token@example.test", Err: cause}
			}, nil)
			require.ErrorIs(t, err, cause)
			require.Contains(t, err.Error(), "cursor upstream transport failed")
			if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
				require.Contains(t, err.Error(), cause.Error(), "shared ops classification must retain the canonical context signal")
			}
			require.NotContains(t, err.Error(), "private-token")
			resp, err := Messages(t.Context(), "private-token", []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"hello"}]}`), nil, "composer-2.5", func(*http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Post", URL: "https://private-token@example.test", Err: cause}
			})
			require.Nil(t, resp, "transport failures must reach the shared transport owner as errors")
			require.ErrorIs(t, err, cause)
			require.NotContains(t, err.Error(), "private-token")
		})
	}
}

func TestAgentHTTPRejectionDoesNotClassifyUnstructuredBody(t *testing.T) {
	for _, body := range []string{
		`<html>usage policy cyber_policy_review private-token</html>`,
		`{"prompt":"usage policy","action_required":"cyber_policy_review"}`,
		`{"code":"invalid_argument","details":`,
	} {
		rejection := newAgentHTTPRejection(502, []byte(body), "private-token", "native-fixture")
		require.Empty(t, rejection.ProviderMessage)
		require.Empty(t, rejection.ActionRequired)
		require.NotContains(t, rejection.Diagnostic, "private-token")
		require.NotContains(t, rejection.Error(), "usage policy")
	}
}

func TestMessagesPreservesCancellationAfterResponseHeaders(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, phase := range []string{"frame_header", "frame_payload", "error_body", "after_text"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", cause, phase, stream), func(t *testing.T) {
					token := "private-token"
					resp, err := Messages(t.Context(), token, []byte(fmt.Sprintf(`{"model":"composer-2.5","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream)), nil, "composer-2.5", func(*http.Request) (*http.Response, error) {
						reader := iotest.ErrReader(&url.Error{Op: "Read", URL: "https://private-token@example.test", Err: cause})
						status := http.StatusOK
						switch phase {
						case "frame_payload":
							reader = io.MultiReader(bytes.NewReader([]byte{0, 0, 0, 0, 1}), reader)
						case "error_body":
							status = http.StatusBadGateway
						case "after_text":
							var frames bytes.Buffer
							require.NoError(t, writeAgentFrame(&frames, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "hello"}}}))
							reader = io.MultiReader(&frames, reader)
						}
						return &http.Response{StatusCode: status, ProtoMajor: 2, Header: http.Header{}, Body: io.NopCloser(reader)}, nil
					})
					if stream && phase == "after_text" {
						require.NoError(t, err)
						require.NotNil(t, resp)
						payload, readErr := io.ReadAll(resp.Body)
						require.ErrorIs(t, readErr, cause)
						require.NotContains(t, string(payload), token)
						require.NotContains(t, string(payload), "message_stop")
						tier, nativeErr := resp.Body.(*MessagesBody).Outcome() //nolint:errcheck // asserted below
						require.Empty(t, tier)
						require.Error(t, nativeErr)
						require.ErrorIs(t, nativeErr, cause)
						if nativeErr != nil {
							require.NotContains(t, nativeErr.Error(), token)
						}
						require.NoError(t, resp.Body.Close())
						return
					}
					if resp != nil {
						_ = resp.Body.Close()
					}
					require.Nil(t, resp, "cancellation must not become a synthetic HTTP rejection")
					require.ErrorIs(t, err, cause)
					require.NotContains(t, err.Error(), token)
				})
			}
		}
	}
}
