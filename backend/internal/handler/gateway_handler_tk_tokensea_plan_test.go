package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTokenseaAnthropicMessagesUsesPlannedChat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "gpt-5.6-luna"
	account := &service.Account{
		ID: 93, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://agent.tokensea.ai", "api_key": "review-test-key",
			"model_mapping": map[string]any{model: model},
		},
	}
	attachHandlerTestProtocolCapability(t, account, protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions)
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
	request, err := newCanonicalProtocolRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, body)
	require.NoError(t, err)
	snapshot, err := service.ProtocolAccountSnapshot(account, model)
	require.NoError(t, err)
	router := service.NewProtocolRouter()
	plan, err := router.Plan(request, snapshot)
	require.NoError(t, err, "a supported Chat converter must not be rejected by the account platform")
	require.Equal(t, protocolrouter.ProtocolChatCompletions, plan.TargetProtocol())

	repo := &plannedOpenAIShapeAccountRepo{account: account}
	upstream := &plannedOpenAIShapeUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	h := &GatewayHandler{
		protocolRouter:       router,
		gatewayService:       service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
		openAIGatewayService: service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	ctx := service.WithProtocolRouting(c.Request.Context(), router, request)
	c.Request = c.Request.WithContext(ctx)
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), service.PlatformAnthropic)
	require.NoError(t, err)
	result, err := h.executeMessagesSelectedProtocol(c, ctx, &service.AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account, &service.APIKey{}, parsed, service.ChannelMappingResult{}, false)
	require.NoError(t, err, recorder.Body.String())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, plan.Endpoint(), upstream.request.URL.String())
	require.Equal(t, "Bearer review-test-key", upstream.request.Header.Get("Authorization"))
	require.Equal(t, model, gjson.GetBytes(upstream.body, "model").String())
	require.Equal(t, "message", gjson.Get(recorder.Body.String(), "type").String())
	require.Contains(t, recorder.Body.String(), "OK")
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}
