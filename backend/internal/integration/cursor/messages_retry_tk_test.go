package cursor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func cursorRetryMessagesTransport(t *testing.T, calls *int, requestIDs *[]string, success ...*pb.AgentServerMessage) func(*http.Request) (*http.Response, error) {
	t.Helper()
	successDo := messagesTestTransport(t, success...)
	return func(req *http.Request) (*http.Response, error) {
		*calls++
		*requestIDs = append(*requestIDs, req.Header.Get("X-Request-Id"))
		if *calls == 1 {
			raw, err := json.Marshal(map[string]any{"code": "invalid_argument", "message": "Conversation data missing"})
			require.NoError(t, err)
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		}
		return successDo(req)
	}
}

func cursorAlwaysRejectingTransport(calls *int, code, message string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		*calls++
		raw, _ := json.Marshal(map[string]any{"code": code, "message": message})
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	}
}

func cursorStreamingThenRejectTransport(t *testing.T, calls *int, requestIDs *[]string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, writeAgentFrame(&data, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "partial"}}}))
	trailer := []byte(`{"error":{"code":"invalid_argument","message":"Conversation data missing"}}`)
	header := [5]byte{2}
	binary.BigEndian.PutUint32(header[1:], uint32(len(trailer)))
	_, _ = data.Write(header[:])
	_, _ = data.Write(trailer)
	return func(req *http.Request) (*http.Response, error) {
		*calls++
		*requestIDs = append(*requestIDs, req.Header.Get("X-Request-Id"))
		return &http.Response{StatusCode: http.StatusOK, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(data.Bytes()))}, nil
	}
}

func TestMessagesContinuationDataMissingRetriesInFreshConversation(t *testing.T) {
	body := []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"hello"}]}`)
	calls := 0
	var requestIDs []string
	resp, err := Messages(context.Background(), "test-token", body, nil, "composer-2.5", cursorRetryMessagesTransport(t, &calls, &requestIDs,
		&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "OK"}}},
		&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(3), OutputTokens: proto.Int64(1), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)}}},
	))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Contains(t, string(raw), `"text":"OK"`)
	require.Equal(t, 2, calls)
	require.Len(t, requestIDs, 2)
	require.NotEqual(t, requestIDs[0], requestIDs[1])
	require.NoError(t, resp.Body.Close())
}

func TestMessagesSupplierError57ContinuationRetriesInFreshConversation(t *testing.T) {
	body := []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"continue"}]}`)
	calls := 0
	var requestIDs []string
	do := func(req *http.Request) (*http.Response, error) {
		calls++
		requestIDs = append(requestIDs, req.Header.Get("X-Request-Id"))
		if calls == 1 {
			raw := []byte(`{"code":"invalid_argument","message":"ResumeAction unavailable: Cursor supplier error 57"}`)
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		}
		var data bytes.Buffer
		require.NoError(t, writeAgentFrame(&data, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "fresh"}}}))
		require.NoError(t, writeAgentFrame(&data, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(3), OutputTokens: proto.Int64(1), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)}}}))
		return &http.Response{StatusCode: http.StatusOK, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(data.Bytes()))}, nil
	}
	resp, err := Messages(context.Background(), "test-token", body, nil, "composer-2.5", do)
	require.NoError(t, err)
	raw, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Contains(t, string(raw), `"text":"fresh"`)
	require.Equal(t, 2, calls)
	require.Len(t, requestIDs, 2)
	require.NotEqual(t, requestIDs[0], requestIDs[1])
	require.NoError(t, resp.Body.Close())
}

func TestClassifyCursorContinuationRetry(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want cursorContinuationRetryKind
	}{
		{name: "conversation data missing", err: newAgentRejection(http.StatusBadRequest, "invalid_argument", "Conversation data missing", "", "req-a"), want: cursorContinuationRetryConversationDataMissing},
		{name: "missing blobs", err: newAgentRejection(http.StatusBadRequest, "failed_precondition", "missing blobs for history", "", "req-a"), want: cursorContinuationRetryConversationDataMissing},
		{name: "supplier error 57", err: newAgentRejection(http.StatusBadRequest, "invalid_argument", "ResumeAction unavailable: Cursor supplier error 57", "", "req-a"), want: cursorContinuationRetryContinuationFailure},
		{name: "policy action required", err: &AgentRejection{Code: "failed_precondition", Diagnostic: "Conversation data missing", ProviderMessage: "Review Data Policy", ActionRequired: "data_retention_consent", Cause: &Error{Status: http.StatusBadRequest}}, want: cursorContinuationRetryNone},
		{name: "plain text is not structured rejection", err: errors.New("Conversation data missing"), want: cursorContinuationRetryNone},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, classifyCursorContinuationRetry(tt.err))
		})
	}
}

func TestMessagesContinuationRetrySkipsAfterStreamingStarted(t *testing.T) {
	body := []byte(`{"model":"composer-2.5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	calls := 0
	var requestIDs []string
	resp, err := Messages(context.Background(), "test-token", body, nil, "composer-2.5", cursorStreamingThenRejectTransport(t, &calls, &requestIDs))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(raw), `"text":"partial"`)
	require.Contains(t, string(raw), `"type":"error"`)
	require.Equal(t, 1, calls)
	require.Len(t, requestIDs, 1)
	require.NoError(t, resp.Body.Close())
}

func TestMessagesContinuationRetryFailureSanitizesAndDoesNotLoop(t *testing.T) {
	body := []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"hello"}]}`)
	calls := 0
	resp, err := Messages(context.Background(), "test-token", body, nil, "composer-2.5", cursorAlwaysRejectingTransport(&calls, "invalid_argument", "Conversation data missing"))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	raw, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.NotContains(t, string(raw), "Conversation data missing")
	require.Equal(t, 2, calls)
	require.NoError(t, resp.Body.Close())
}
