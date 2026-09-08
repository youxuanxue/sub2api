package cursor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func messagesTestTransport(t *testing.T, messages ...*pb.AgentServerMessage) func(*http.Request) (*http.Response, error) {
	t.Helper()
	var data bytes.Buffer
	for _, message := range messages {
		require.NoError(t, writeAgentFrame(&data, message))
	}
	if len(messages) > 0 && messages[len(messages)-1].ExecServerMessage != nil {
		trailer := []byte(`{"error":{"code":"canceled"}}`)
		header := [5]byte{2}
		binary.BigEndian.PutUint32(header[1:], uint32(len(trailer)))
		_, _ = data.Write(header[:])
		_, _ = data.Write(trailer)
	}
	return func(req *http.Request) (*http.Response, error) {
		require.Equal(t, AgentBaseURL+agentRunPath, req.URL.String())
		return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(bytes.NewReader(data.Bytes()))}, nil
	}
}
func TestMessagesReportedUsageAndStreaming(t *testing.T) {
	for _, stream := range []bool{false, true} {
		body, err := json.Marshal(map[string]any{"model": "composer-2.5", "stream": stream, "messages": []map[string]any{{"role": "user", "content": "hello"}}})
		require.NoError(t, err)
		resp, err := Messages(context.Background(), "test-token", body, nil, "composer-2.5", messagesTestTransport(t,
			&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "OK"}}},
			&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 30, CacheWriteTokens: 5}}},
		))
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		output, ok := resp.Body.(*MessagesBody)
		require.True(t, ok)
		tier, err := output.Outcome()
		require.NoError(t, err)
		require.Equal(t, ReportedBillingTier, tier)
		require.Contains(t, string(raw), `"input_tokens":10`)
		require.Contains(t, string(raw), `"cache_read_input_tokens":30`)
		require.Contains(t, string(raw), `"tk_billing_tier":"cursor-oauth-reported"`)
		if stream {
			require.Contains(t, string(raw), "event: message_stop")
			require.Contains(t, string(raw), `"text":"OK"`)
		}
		require.NoError(t, resp.Body.Close())
	}
}
func TestMessagesRejectsUnverifiedSystemBeforeUpstream(t *testing.T) {
	for _, system := range []string{`"Follow the caller's instructions"`, `[{"type":"text","text":"Follow the caller's instructions"}]`} {
		resp, err := Messages(t.Context(), "test-token", []byte(`{"model":"composer-2.5","system":`+system+`,"messages":[{"role":"user","content":"hello"}]}`), nil, "composer-2.5", func(*http.Request) (*http.Response, error) {
			t.Fatal("unverified system instructions must not reach inference")
			return nil, nil
		})
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
}
func TestMessagesToolHandoffEstimatesOnlyMissingUsage(t *testing.T) {
	body := []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"Lookup demo"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
	resp, err := Messages(context.Background(), "test-token", body, nil, "composer-2.5", messagesTestTransport(t,
		&pb.AgentServerMessage{ExecServerMessage: &pb.ExecServerMessage{McpArgs: &pb.McpArgs{Name: cursorWireToolName("lookup"), ToolCallId: "call_a", Args: map[string]*structpb.Value{"key": structpb.NewStringValue("demo")}}}},
	))
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	output, ok := resp.Body.(*MessagesBody)
	require.True(t, ok)
	tier, err := output.Outcome()
	require.NoError(t, err)
	require.Equal(t, EstimatedBillingTier, tier)
	require.Contains(t, string(raw), `"tk_billing_tier":"cursor-oauth-estimated"`)
	var message struct {
		Usage AgentUsage `json:"usage"`
		Stop  string     `json:"stop_reason"`
	}
	require.NoError(t, json.Unmarshal(raw, &message))
	require.Positive(t, message.Usage.Input)
	require.Positive(t, message.Usage.Output)
	require.Zero(t, message.Usage.CacheRead)
	require.Zero(t, message.Usage.CacheWrite)
	require.Equal(t, "tool_use", message.Stop)
	require.NoError(t, resp.Body.Close())
}
func TestMessagesTruncationIsNotSuccessfulCompletion(t *testing.T) {
	for _, stream := range []bool{false, true} {
		body := []byte(`{"model":"composer-2.5","stream":` + map[bool]string{true: "true", false: "false"}[stream] + `,"messages":[{"role":"user","content":"hi"}]}`)
		resp, err := Messages(context.Background(), "test", body, nil, "composer-2.5", messagesTestTransport(t, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "partial"}}}))
		require.NoError(t, err)
		raw, readErr := io.ReadAll(resp.Body)
		if stream {
			require.Error(t, readErr)
			require.NotContains(t, string(raw), "event: message_stop")
		} else {
			require.Equal(t, 502, resp.StatusCode)
		}
		require.NoError(t, resp.Body.Close())
	}
}
func TestMessagesClosingConsumerCancelsProducer(t *testing.T) {
	resp, err := Messages(context.Background(), "test", []byte(`{"model":"composer-2.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`), nil, "composer-2.5", messagesTestTransport(t, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: strings.Repeat("a", 10000)}}}))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}
