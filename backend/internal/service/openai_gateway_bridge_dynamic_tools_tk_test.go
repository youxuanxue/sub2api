//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// These service bridges use a separate converter from apicompat's Chat bridge.
// Reject before dispatch rather than silently dropping the tool history.
func TestAnthropicChatBridgesRejectDynamicToolsBeforeUpstream(t *testing.T) {
	for _, change := range []string{"tool_addition", "tool_removal"} {
		for _, path := range []string{"newapi", "grok_fallback"} {
			t.Run(path+"/"+change, func(t *testing.T) {
				var upstreamCalls atomic.Int32
				upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					upstreamCalls.Add(1)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer upstreamServer.Close()
				upstream := &httpUpstreamRecorder{}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				account := &Account{
					ID: 1, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
					ChannelType: newapiconstant.ChannelTypeDeepSeek,
					Credentials: map[string]any{"api_key": "test-key", "base_url": upstreamServer.URL},
				}
				body := []byte(`{"model":"test-model","max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":[{"type":"` + change + `","tool":{"type":"tool_definition","definition":{"name":"late","input_schema":{"type":"object"}}}}]}]}`)
				var req apicompat.AnthropicRequest
				require.NoError(t, json.Unmarshal(body, &req))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
				var result *OpenAIForwardResult
				var err error
				if path == "newapi" {
					require.True(t, svc.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions))
					result, err = svc.ForwardAsAnthropicDispatched(context.Background(), c, account, body, "", "")
				} else {
					account.Platform = PlatformGrok
					result, err = svc.fallbackAnthropicToGrokChatCompletions(context.Background(), c, account, &req, false, req.Model, req.Model, req.Model, "test-token", "", time.Now())
				}
				require.Nil(t, result)
				require.ErrorContains(t, err, "dynamic tool changes require the Responses bridge")
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Contains(t, rec.Body.String(), "invalid_request_error")
				require.Zero(t, upstreamCalls.Load(), "NewAPI bridge must not dispatch")
				require.Nil(t, upstream.lastReq, "Grok fallback must not dispatch")
			})
		}
	}
}
