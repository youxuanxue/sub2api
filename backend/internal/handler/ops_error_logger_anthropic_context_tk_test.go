//go:build unit

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS062_AnthropicContextErrorOutcome(t *testing.T) {
	const original = "输入内容过长，请减少 Prompt 中的 token 数量后重试"
	for _, scenario := range []string{"fast_rejection", "heartbeat_rejection", "recovered_success"} {
		t.Run(scenario, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if scenario == "recovered_success" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ok\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-fable-5-1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					return
				}
				if scenario == "heartbeat_rejection" {
					time.Sleep(1100 * time.Millisecond)
				}
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"<nil>","message":"`+original+`"}}`)
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Gateway.StreamKeepaliveInterval = 1
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Security.URLAllowlist.AllowPrivateHosts = true
			account := &service.Account{ID: 136, Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey, ChannelType: 14, Credentials: map[string]any{"base_url": upstream.URL, "api_key": "synthetic-key"}, Extra: map[string]any{openai_compat.ExtraKeyNativeMessagesSupported: true, openai_compat.ExtraKeyResponsesSupported: false}}
			attachHandlerTestProtocolCapability(t, account, protocolrouter.ProtocolMessages)
			repo := &plannedOpenAIShapeAccountRepo{account: account}
			svc := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, candidateNativeHTTPUpstream{baseURL: upstream.URL}, nil, nil, nil, nil, nil, nil, nil, nil)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/messages", func(c *gin.Context) {
				setOpsRequestContext(c, "claude-fable-5-1", true)
				if scenario == "recovered_success" {
					c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{UpstreamStatusCode: 503, Message: "earlier attempt unavailable"}})
				}
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				request, err := newCanonicalProtocolRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, "claude-fable-5-1", true, body)
				require.NoError(t, err)
				snapshot, err := service.ProtocolAccountSnapshot(account, "claude-fable-5-1")
				require.NoError(t, err)
				planner := service.NewProtocolRouter()
				plan, err := planner.Plan(request, snapshot)
				require.NoError(t, err)
				ctx := service.WithProtocolRouting(c.Request.Context(), planner, request)
				c.Request = c.Request.WithContext(ctx)
				parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), service.PlatformAnthropic)
				require.NoError(t, err)
				h := &GatewayHandler{protocolRouter: planner, openAIGatewayService: svc, gatewayService: service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, candidateNativeHTTPUpstream{baseURL: upstream.URL}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)}
				result, err := h.executeMessagesSelectedProtocol(c, ctx, &service.AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account, &service.APIKey{}, parsed, service.ChannelMappingResult{}, true)
				if scenario == "recovered_success" {
					require.NoError(t, err)
					require.Equal(t, 1, result.Usage.OutputTokens)
				} else {
					require.Error(t, err)
					require.Nil(t, result)
				}
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-fable-5-1","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)))
			require.Equal(t, int64(1), OpsErrorLogQueueLength(), "exactly one terminal/recovered record")
			job := <-opsErrorLogQueue
			if scenario == "recovered_success" {
				require.Equal(t, 200, rec.Code)
				require.Contains(t, rec.Body.String(), "OK")
				require.Equal(t, 200, job.entry.StatusCode)
				require.Equal(t, "Recovered upstream error 503: earlier attempt unavailable", job.entry.ErrorMessage)
			} else {
				wantHTTP := 400
				if scenario == "heartbeat_rejection" {
					wantHTTP = 200
				}
				require.Equal(t, wantHTTP, rec.Code)
				require.Equal(t, 400, job.entry.StatusCode)
				require.Equal(t, "invalid_request_error", job.entry.ErrorType)
				require.Equal(t, "client", job.entry.ErrorOwner)
				require.NotContains(t, job.entry.ErrorMessage, "Recovered")
				require.Contains(t, job.entry.ErrorMessage, "prompt is too long")
				require.NotNil(t, job.entry.UpstreamErrorMessage)
				require.NotNil(t, job.entry.UpstreamErrorsJSON)
				events, parseErr := service.ParseOpsUpstreamErrors(*job.entry.UpstreamErrorsJSON)
				require.NoError(t, parseErr)
				require.Equal(t, original, events[len(events)-1].Message)
			}
		})
	}
}
