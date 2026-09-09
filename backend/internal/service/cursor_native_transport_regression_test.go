//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type cursorNativeTestUpstream struct {
	protocolTargetHTTPUpstream
	run      *pb.AgentRunRequest
	response []byte
}

func (u *cursorNativeTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req.URL.String() != cursor.AgentBaseURL+"/agent.v1.AgentService/Run" || HTTPUpstreamProfileFromContext(req.Context()) != HTTPUpstreamProfileCursor {
		return nil, fmt.Errorf("request bypassed Cursor native transport")
	}
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
			for _, outcome := range []string{"reported", "handoff", "incomplete"} {
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
					} else {
						frame(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "NATIVE_OK"}}})
					}
					usage := &pb.TurnEndedUpdate{}
					if outcome == "reported" {
						usage = &pb.TurnEndedUpdate{InputTokens: proto.Int64(11), OutputTokens: proto.Int64(3), CacheReadTokens: proto.Int64(7), CacheWriteTokens: proto.Int64(2)}
					}
					frame(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: usage}})
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
					if outcome == "incomplete" {
						require.Error(t, err, "an incomplete native run must not be billed as successful")
						return
					}
					require.NoError(t, err)
					require.Equal(t, model, upstream.run.GetRequestedModel().GetModelId())
					require.Equal(t, "mcp__tokenkey__lookup", upstream.run.GetMcpTools().GetMcpTools()[0].GetName())
					require.NotNil(t, result)
					if outcome == "reported" {
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
