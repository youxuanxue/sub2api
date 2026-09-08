package cursor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestAgentHistoryReconstructsNativeToolTurns(t *testing.T) {
	input := AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{
		{Role: "user", Text: "lookup demo"},
		{Role: "assistant", ToolCalls: []AgentToolCall{{ID: "call_a", Name: "lookup", Arguments: map[string]any{"key": "demo"}}}},
		{Role: "tool", ToolCallID: "call_a", Text: "739281"},
	}}
	run, blobs, err := buildAgentRun(input)
	require.NoError(t, err)
	require.NotNil(t, run.Action.ResumeAction)
	require.Nil(t, run.Action.UserMessageAction, "tool results must not synthesize an empty user message")
	require.Len(t, run.ConversationState.Turns, 1)
	turn := &pb.ConversationTurnStructure{}
	require.NoError(t, proto.Unmarshal(blobs.data[string(run.ConversationState.Turns[0])], turn))
	user := &pb.UserMessage{}
	require.NoError(t, proto.Unmarshal(blobs.data[string(turn.AgentConversationTurn.UserMessage)], user))
	require.Equal(t, "lookup demo", user.Text)
	step := &pb.ConversationStep{}
	require.NoError(t, proto.Unmarshal(blobs.data[string(turn.AgentConversationTurn.Steps[0])], step))
	require.Equal(t, "739281", step.ToolCall.McpToolCall.Result.Success.Content[0].Text.Text)
	require.Equal(t, "demo", step.ToolCall.McpToolCall.Args.Args["key"].GetStringValue())
	again, nextBlobs, err := buildAgentRun(input)
	require.NoError(t, err)
	require.NotEqual(t, run.ConversationId, again.ConversationId)
	require.Equal(t, len(blobs.data), len(nextBlobs.data))
	for _, id := range again.ConversationState.RootPromptMessagesJson {
		require.Contains(t, nextBlobs.data, string(id))
	}
}

func TestAgentRejectsOrphanToolHistory(t *testing.T) {
	for _, messages := range [][]AgentMessage{
		{{Role: "tool", ToolCallID: "orphan", Text: "result"}},
		{{Role: "user", Text: "question"}, {Role: "assistant", ToolCalls: []AgentToolCall{{ID: "missing", Name: "lookup"}}}},
	} {
		_, _, err := buildAgentRun(AgentRequest{Model: "composer-2.5", Messages: messages})
		require.Error(t, err)
	}
}

func TestAgentRejectsOversizedAndTruncatedFrames(t *testing.T) {
	var header [5]byte
	binary.BigEndian.PutUint32(header[1:], maxAgentFrame+1)
	_, _, err := readAgentFrame(bytes.NewReader(header[:]))
	require.ErrorContains(t, err, "too large")
	binary.BigEndian.PutUint32(header[1:], 3)
	_, _, err = readAgentFrame(bytes.NewReader(append(header[:], byte(1))))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestAgentRequiresTerminalUsage(t *testing.T) {
	var stream bytes.Buffer
	require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
		TextDelta: &pb.TextDeltaUpdate{Text: "partial"}}}))
	_, err := RunAgent(context.Background(), "test-credential", AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "hello"}}},
		func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&stream)}, nil
		}, nil)
	require.ErrorContains(t, err, "interrupted")
}

func TestAgentUsagePreservesCacheBuckets(t *testing.T) {
	var stream bytes.Buffer
	require.NoError(t, writeAgentFrame(&stream, &pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
		TurnEnded: &pb.TurnEndedUpdate{InputTokens: 11, OutputTokens: 3, CacheReadTokens: 7, CacheWriteTokens: 2}}}))
	result, err := RunAgent(context.Background(), "test-credential", AgentRequest{Model: "composer-2.5", Messages: []AgentMessage{{Role: "user", Text: "hello"}}},
		func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "mcp_tool_call", req.Header.Get("X-Cursor-Agent-Allowed-Tools"))
			return &http.Response{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&stream)}, nil
		}, nil)
	require.NoError(t, err)
	require.Equal(t, &AgentUsage{Input: 11, Output: 3, CacheRead: 7, CacheWrite: 2}, result.Usage)
}

// Opt-in direct protocol integration probe. It runs no CLI or SDK and never logs
// credentials. This is not a TokenKey UI or end-to-end acceptance test.
func TestAgentLive(t *testing.T) {
	if os.Getenv("TOKENKEY_CURSOR_LIVE_PROBE") != "1" {
		t.Skip("real-account probe requires explicit opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	credential, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-a", "cursor-user", "-s", "cursor-access-token", "-w").Output()
	require.NoError(t, err)
	token := strings.TrimSpace(string(credential))
	transport := &http.Transport{ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	model := os.Getenv("TOKENKEY_CURSOR_LIVE_MODEL")
	if model == "" {
		model = "composer-2.5"
	}
	input := AgentRequest{Model: model, Parameters: []Parameter{{ID: "fast", Value: "false"}},
		System:   "Reply with exactly CURSOR_GO_OAUTH_OK. Do not use tools.",
		Messages: []AgentMessage{{Role: "user", Text: "Follow the system instruction."}}}
	result, err := RunAgent(ctx, token, input, client.Do, nil)
	require.NoError(t, err)
	require.Equal(t, "CURSOR_GO_OAUTH_OK", strings.TrimSpace(result.Text))
	require.NotNil(t, result.Usage)
	raw, _ := json.Marshal(result)
	t.Logf("direct_text: %s", raw)
	input.System = ""
	input.Tools = []AgentTool{{Name: "lookup", Description: "Look up the value for a key.", Schema: map[string]any{
		"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []any{"key"},
	}}}
	input.Messages = []AgentMessage{{Role: "user", Text: "Call lookup with key demo. After receiving its result, reply with only that result. Do not use other tools."}}
	result, err = RunAgent(ctx, token, input, client.Do, nil)
	require.NoError(t, err)
	require.True(t, result.ToolHandoff)
	require.Len(t, result.ToolCalls, 1)
	require.Equal(t, "demo", result.ToolCalls[0].Arguments["key"])
	raw, _ = json.Marshal(result)
	t.Logf("direct_tool_handoff: %s", raw)
	input.Messages = append(input.Messages, AgentMessage{Role: "assistant", Text: result.Text, ToolCalls: result.ToolCalls},
		AgentMessage{Role: "tool", ToolCallID: result.ToolCalls[0].ID, Text: "TK_GO_STATELESS_739281"})
	// A different HTTP transport proves continuation does not retain the stream.
	transport.CloseIdleConnections()
	nextTransport := &http.Transport{ForceAttemptHTTP2: true}
	defer nextTransport.CloseIdleConnections()
	nextClient := &http.Client{Transport: nextTransport, CheckRedirect: client.CheckRedirect}
	result, err = RunAgent(ctx, token, input, nextClient.Do, nil)
	require.NoError(t, err)
	require.Contains(t, result.Text, "TK_GO_STATELESS_739281")
	require.False(t, result.ToolHandoff)
	require.NotNil(t, result.Usage)
	raw, _ = json.Marshal(result)
	t.Logf("direct_tool_resume: %s", raw)
}

func TestAgentLiveCatalog(t *testing.T) {
	if os.Getenv("TOKENKEY_CURSOR_LIVE_CATALOG") != "1" {
		t.Skip("real catalog probe requires explicit opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	credential, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-a", "cursor-user", "-s", "cursor-access-token", "-w").Output()
	require.NoError(t, err)
	token := strings.TrimSpace(string(credential))
	transport := &http.Transport{ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	models, err := OAuthModels(ctx, token, client.Do)
	require.NoError(t, err)
	for _, model := range models {
		t.Run(model.ID, func(t *testing.T) {
			probeCtx, stop := context.WithTimeout(ctx, 45*time.Second)
			defer stop()
			input := AgentRequest{Model: model.ID, Parameters: DefaultParameters(model),
				Messages: []AgentMessage{{Role: "user", Text: "Reply with exactly CURSOR_GO_CATALOG_OK. Do not use tools."}}}
			input.WireModel, err = AgentVariantWireModel(model, input.Parameters)
			require.NoError(t, err)
			result, err := RunAgent(probeCtx, token, input, client.Do, nil)
			require.NoError(t, err)
			require.Equal(t, "CURSOR_GO_CATALOG_OK", strings.TrimSpace(result.Text))
			require.NotNil(t, result.Usage)
			raw, _ := json.Marshal(result)
			t.Logf("catalog_model=%s parameters=%v result=%s", model.ID, input.Parameters, raw)
		})
	}
}
