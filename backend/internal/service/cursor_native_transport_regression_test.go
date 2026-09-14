//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type cursorNativeTestUpstream struct {
	protocolTargetHTTPUpstream
	calls    int
	run      *pb.AgentRunRequest
	response []byte
}

func (u *cursorNativeTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req.URL.String() != cursor.AgentBaseURL+"/agent.v1.AgentService/Run" || HTTPUpstreamProfileFromContext(req.Context()) != HTTPUpstreamProfileCursor {
		return nil, fmt.Errorf("request bypassed Cursor native transport")
	}
	u.calls++
	var header [5]byte
	if _, err := io.ReadFull(req.Body, header[:]); err != nil {
		return nil, err
	}
	data := make([]byte, binary.BigEndian.Uint32(header[1:]))
	if _, err := io.ReadFull(req.Body, data); err != nil {
		return nil, err
	}
	var message pb.AgentClientMessage
	if err := proto.Unmarshal(data, &message); err != nil {
		return nil, err
	}
	u.run = message.RunRequest
	return &http.Response{StatusCode: 200, ProtoMajor: 2, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(u.response))}, nil
}

func TestCursorProtocolRoutesUseNativeTransportAndSettlement(t *testing.T) {
	for _, inbound := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
		for _, stream := range []bool{false, true} {
			for _, outcome := range []string{"reported", "handoff", "resumed", "incomplete", "cyber_early", "cyber_late", "usage_early", "usage_late"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", inbound, stream, outcome), func(t *testing.T) {
					const model = "composer-2.5"
					body := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":1,"messages":[{"role":"user","content":"lookup"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`, model, stream))
					path := protocolrouter.ResponsesPathNone
					if inbound == protocolrouter.ProtocolChatCompletions {
						body = []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"max_completion_tokens":1,"messages":[{"role":"user","content":"lookup"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`, model, stream))
					} else if inbound == protocolrouter.ProtocolResponses {
						body = []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"max_output_tokens":1,"input":"lookup","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`, model, stream))
						path = protocolrouter.ResponsesPathRoot
					}
					if outcome == "resumed" || strings.Contains(outcome, "_early") || strings.Contains(outcome, "_late") {
						switch inbound {
						case protocolrouter.ProtocolMessages:
							body = []byte(strings.Replace(string(body), `"messages":[{"role":"user","content":"lookup"}]`, `"messages":[{"role":"user","content":"lookup"},{"role":"assistant","content":[{"type":"tool_use","id":"call_native","name":"lookup","input":{"key":"demo"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_native","content":"NATIVE_NONCE_739281"}]}]`, 1))
						case protocolrouter.ProtocolChatCompletions:
							body = []byte(strings.Replace(string(body), `"messages":[{"role":"user","content":"lookup"}]`, `"messages":[{"role":"user","content":"lookup"},{"role":"assistant","tool_calls":[{"id":"call_native","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"demo\"}"}}]},{"role":"tool","tool_call_id":"call_native","content":"NATIVE_NONCE_739281"}]`, 1))
						default:
							body = []byte(strings.Replace(string(body), `"input":"lookup"`, `"input":[{"role":"user","content":"lookup"},{"type":"function_call","call_id":"call_native","name":"lookup","arguments":"{\"key\":\"demo\"}"},{"type":"function_call_output","call_id":"call_native","output":"NATIVE_NONCE_739281"}]`, 1))
						}
					}
					request, err := protocolrouter.ParseCanonicalRequest(inbound, path, model, stream, body)
					require.NoError(t, err)
					account := cursorCandidateAccount(model)
					router := NewProtocolRouter()
					ctx := WithProtocolRouting(t.Context(), router, request)
					plan, _, err := protocolPlanForAccount(ctx, account, model)
					require.NoError(t, err)
					var response bytes.Buffer
					frame := func(message proto.Message) {
						raw, err := proto.Marshal(message)
						require.NoError(t, err)
						header := [5]byte{}
						binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
						response.Write(header[:])
						response.Write(raw)
					}
					if outcome == "handoff" {
						frame(&pb.AgentServerMessage{ExecServerMessage: &pb.ExecServerMessage{McpArgs: &pb.McpArgs{Name: "mcp__tokenkey__lookup", ToolCallId: "call_native"}}})
					} else if !strings.HasSuffix(outcome, "_early") {
						frame(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "NATIVE_OK"}}})
					}
					usage := &pb.TurnEndedUpdate{}
					if outcome == "reported" || outcome == "resumed" {
						usage = &pb.TurnEndedUpdate{InputTokens: proto.Int64(11), OutputTokens: proto.Int64(3), CacheReadTokens: proto.Int64(7), CacheWriteTokens: proto.Int64(2)}
					}
					if strings.HasPrefix(outcome, "cyber_") || strings.HasPrefix(outcome, "usage_") {
						response.Write(cursorPolicyConnectFrame(t, strings.HasPrefix(outcome, "cyber_")))
					} else {
						frame(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: usage}})
					}
					upstream := &cursorNativeTestUpstream{response: response.Bytes()}
					svc := protocolTargetTestService(nil)
					svc.httpUpstream = upstream
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, protocolRouteContractInboundPath(inbound), bytes.NewReader(body))
					var result *OpenAIForwardResult
					_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
						func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(account),
						protocolExecutorsForTest(plan, func(ctx context.Context, a *Account, _ protocolrouter.Plan, r protocolrouter.CanonicalRequest) (any, error) {
							var err error
							switch inbound {
							case protocolrouter.ProtocolMessages:
								result, err = svc.ForwardAsAnthropic(ctx, c, a, r.Body(), "", "")
							case protocolrouter.ProtocolChatCompletions:
								result, err = svc.ForwardAsChatCompletions(ctx, c, a, r.Body(), "", "")
							default:
								result, err = svc.Forward(ctx, c, a, r.Body())
							}
							return result, err
						}))
					if strings.HasPrefix(outcome, "cyber_") || strings.HasPrefix(outcome, "usage_") {
						require.ErrorIs(t, err, errOpenAICyberPolicyForwarded)
						require.Nil(t, result)
						var failover *UpstreamFailoverError
						require.False(t, errors.As(err, &failover))
						require.Equal(t, 1, upstream.calls)
						require.True(t, IsResponseCommitted(c))
						if strings.HasPrefix(outcome, "cyber_") {
							require.NotNil(t, GetOpsCyberPolicy(c))
							require.Nil(t, GetOpsUsagePolicy(c))
						} else {
							require.NotNil(t, GetOpsUsagePolicy(c))
							require.Nil(t, GetOpsCyberPolicy(c))
						}
						require.Equal(t, 1, strings.Count(recorder.Body.String(), `"error":`))
						require.NotContains(t, recorder.Body.String(), `"status":"completed"`)
						require.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
						require.NotContains(t, recorder.Body.String(), "cyber_policy_review")
						require.NotContains(t, recorder.Body.String(), "message_stop")
						return
					}
					if outcome == "incomplete" {
						require.Error(t, err, "an incomplete native run must not be billed as successful")
						require.Nil(t, result, "missing native settlement must not create usage")
						require.NotContains(t, recorder.Body.String(), `"status":"completed"`)
						require.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
						if stream && inbound == protocolrouter.ProtocolMessages {
							require.True(t, IsResponseCommitted(c), "native terminal error must not receive a second handler error")
						}
						if !stream && inbound != protocolrouter.ProtocolMessages {
							require.False(t, c.Writer.Written())
						}
						return
					}
					require.NoError(t, err)
					require.Equal(t, model, upstream.run.GetRequestedModel().GetModelId())
					require.Equal(t, "mcp__tokenkey__lookup", upstream.run.GetMcpTools().GetMcpTools()[0].GetName())
					if outcome == "resumed" {
						require.NotNil(t, upstream.run.GetAction().GetResumeAction())
						blobs := make(map[string][]byte)
						for _, blob := range upstream.run.PreFetchedBlobs {
							blobs[string(blob.Id)] = blob.Value
						}
						require.Len(t, upstream.run.ConversationState.Turns, 1)
						var turn pb.ConversationTurnStructure
						require.NoError(t, proto.Unmarshal(blobs[string(upstream.run.ConversationState.Turns[0])], &turn))
						require.Len(t, turn.AgentConversationTurn.Steps, 1)
						var step pb.ConversationStep
						require.NoError(t, proto.Unmarshal(blobs[string(turn.AgentConversationTurn.Steps[0])], &step))
						require.Equal(t, "NATIVE_NONCE_739281", step.ToolCall.McpToolCall.Result.Success.Content[0].Text.Text)
						require.Equal(t, "demo", step.ToolCall.McpToolCall.Args.Args["key"].GetStringValue())
					}
					require.NotNil(t, result)
					if outcome == "reported" || outcome == "resumed" {
						require.Equal(t, cursor.ReportedBillingTier, result.BillingTier)
						require.Equal(t, 20, result.Usage.InputTokens, "OpenAI input includes fresh and cached buckets")
						require.Equal(t, 7, result.Usage.CacheReadInputTokens)
						require.Equal(t, 3, result.Usage.OutputTokens, "unsupported output limits do not truncate or cap settlement")
						require.Contains(t, recorder.Body.String(), "NATIVE_OK")
					} else {
						require.Equal(t, cursor.EstimatedBillingTier, result.BillingTier)
						require.Positive(t, result.Usage.OutputTokens)
						require.Contains(t, recorder.Body.String(), "call_native")
					}
				})
			}
		}
	}
}

// The cyber fixture is the retained supplier 13 detail. Usage-only removes just
// the analytics action, exercising the existing structured-text policy owner.
func cursorPolicyConnectFrame(t *testing.T, cyber bool) []byte {
	t.Helper()
	detail := "CA0SlgEKHFJlcXVlc3QgYmxvY2tlZCBieSBBbnRocm9waWMSXVdlIGFyZSB1bmFibGUgdG8gY29tcGxldGUgdGhpcyByZXF1ZXN0IGJlY2F1c2UgaXQgd2FzIGJsb2NrZWQgdW5kZXIgQW50aHJvcGljJ3MgVXNhZ2UgUG9saWN5LiAAUhUKE2N5YmVyX3BvbGljeV9yZXZpZXcYAQ"
	if !cyber {
		custom := protowire.AppendTag(nil, 2, protowire.BytesType)
		custom = protowire.AppendString(custom, "Request blocked under Anthropic's Usage Policy.")
		raw := protowire.AppendTag(nil, 2, protowire.BytesType)
		raw = protowire.AppendBytes(raw, custom)
		detail = base64.RawStdEncoding.EncodeToString(raw)
	}
	raw, err := json.Marshal(gin.H{"error": gin.H{"code": "invalid_argument", "message": "Error", "details": []any{gin.H{"type": "aiserver.v1.ErrorDetails", "value": detail}}}})
	require.NoError(t, err)
	header := [5]byte{2}
	binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
	return append(header[:], raw...)
}
