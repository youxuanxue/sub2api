//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Regression: reproduce the production failure, without a live upstream.
func TestCursorRegressionBufferedPartialBeforeNativeError(t *testing.T) {
	const model = "composer-2.5"
	body := []byte(`{"model":"composer-2.5","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, model, false, body)
	require.NoError(t, err)
	account := cursorCandidateAccount(model)
	router := NewProtocolRouter()
	ctx := WithProtocolRouting(t.Context(), router, request)
	plan, _, err := protocolPlanForAccount(ctx, account, model)
	require.NoError(t, err)
	var frames bytes.Buffer
	raw, err := proto.Marshal(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "I"}}})
	require.NoError(t, err)
	header := [5]byte{}
	binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
	frames.Write(header[:])
	frames.Write(raw)
	trailer := []byte(`{"error":{"code":"invalid_argument","message":"DIAGNOSTIC_REASON_MUST_NOT_DISAPPEAR"}}`)
	header[0] = 2
	binary.BigEndian.PutUint32(header[1:], uint32(len(trailer)))
	frames.Write(header[:])
	frames.Write(trailer)
	svc := protocolTargetTestService(nil)
	svc.httpUpstream = &cursorNativeTestUpstream{response: frames.Bytes()}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	var result *OpenAIForwardResult
	_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
		func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(account),
		protocolExecutorsForTest(plan, func(ctx context.Context, a *Account, _ protocolrouter.Plan, r protocolrouter.CanonicalRequest) (any, error) {
			var err error
			result, err = svc.ForwardAsChatCompletions(ctx, c, a, r.Body(), "", "")
			return result, err
		}))
	require.ErrorContains(t, err, "Connect invalid_argument, mapped HTTP 400")
	require.NotContains(t, err.Error(), "DIAGNOSTIC_REASON_MUST_NOT_DISAPPEAR")
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
	require.Nil(t, result)

}

func TestCursorRegressionResponsesPartialBeforeNativeError(t *testing.T) {
	const model = "composer-2.5"
	body := []byte(`{"model":"composer-2.5","stream":false,"input":[{"role":"user","content":"hello"}]}`)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolResponses, protocolrouter.ResponsesPathRoot, model, false, body)
	require.NoError(t, err)
	account := cursorCandidateAccount(model)
	router := NewProtocolRouter()
	ctx := WithProtocolRouting(t.Context(), router, request)
	plan, _, err := protocolPlanForAccount(ctx, account, model)
	require.NoError(t, err)
	var frames bytes.Buffer
	raw, err := proto.Marshal(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "I"}}})
	require.NoError(t, err)
	header := [5]byte{}
	binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
	frames.Write(header[:])
	frames.Write(raw)
	trailer := []byte(`{"error":{"code":"invalid_argument","message":"DIAGNOSTIC_REASON_MUST_NOT_DISAPPEAR"}}`)
	header[0] = 2
	binary.BigEndian.PutUint32(header[1:], uint32(len(trailer)))
	frames.Write(header[:])
	frames.Write(trailer)
	svc := protocolTargetTestService(nil)
	svc.httpUpstream = &cursorNativeTestUpstream{response: frames.Bytes()}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	var result *OpenAIForwardResult
	_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
		func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(account),
		protocolExecutorsForTest(plan, func(ctx context.Context, a *Account, _ protocolrouter.Plan, r protocolrouter.CanonicalRequest) (any, error) {
			var err error
			result, err = svc.Forward(ctx, c, a, r.Body())
			return result, err
		}))
	require.ErrorContains(t, err, "Connect invalid_argument, mapped HTTP 400")
	require.NotContains(t, err.Error(), "DIAGNOSTIC_REASON_MUST_NOT_DISAPPEAR")
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
	require.Nil(t, result)

}
