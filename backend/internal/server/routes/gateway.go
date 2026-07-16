package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterGatewayRoutes 注册 API 网关路由（Claude/OpenAI/Gemini 兼容）
func RegisterGatewayRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	cfg *config.Config,
) {
	bodyLimit := middleware.RequestBodyLimit(cfg.Gateway.MaxBodySize)
	clientRequestID := middleware.ClientRequestID()
	trajectoryID := middleware.TrajectoryID()
	opsErrorLogger := handler.OpsErrorLoggerMiddleware(opsService)
	endpointNorm := handler.InboundEndpointMiddleware()
	qaCapture := gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if h != nil && h.QACapture != nil {
		qaCapture = h.QACapture.Middleware()
	}

	// 未分组 Key 拦截中间件（按协议格式区分错误响应）
	requireGroupAnthropic := middleware.RequireGroupAssignment(settingService, middleware.AnthropicErrorWriter)
	requireGroupGoogle := middleware.RequireGroupAssignment(settingService, middleware.GoogleErrorWriter)

	// API网关（Claude API兼容）
	gateway := r.Group("/v1")
	gateway.Use(bodyLimit)
	gateway.Use(clientRequestID)
	gateway.Use(trajectoryID)
	gateway.Use(qaCapture)
	gateway.Use(opsErrorLogger)
	gateway.Use(endpointNorm)
	gateway.Use(gin.HandlerFunc(apiKeyAuth))
	gateway.GET("/sub2api/billing", h.Gateway.KeyBillingInfo)
	gateway.Use(requireGroupAnthropic)
	{
		// /v1/messages: auto-route based on group platform
		gateway.POST("/messages", tkOpenAICompatMessagesPOST(h))
		// /v1/messages/count_tokens: OpenAI uses Anthropic-compat bridge; other
		// OpenAI-compatible platforms keep the prior unsupported response.
		gateway.POST("/messages/count_tokens", tkOpenAICompatCountTokensPOST(h))
		gateway.GET("/models", h.Gateway.Models)
		gateway.GET("/usage", h.Gateway.Usage)
		// OpenAI Responses API: auto-route based on group platform
		gateway.POST("/responses", tkOpenAICompatResponsesPOST(h))
		gateway.POST("/responses/*subpath", tkOpenAICompatResponsesPOST(h))
		gateway.GET("/responses", tkOpenAICompatResponsesWebSocketGET(h))
		// OpenAI Chat Completions API: auto-route based on group platform
		gateway.POST("/chat/completions", tkOpenAICompatChatCompletionsPOST(h))
		gateway.POST("/embeddings", tkOpenAICompatEmbeddingsHandler(h))
		gateway.POST("/images/generations", tkOpenAICompatImageGenerationsHandler(h))
		gateway.POST("/images/edits", tkOpenAICompatImageEditsHandler(h))
		// TK: re-mint a short-lived presigned URL for an already-offloaded image
		// (the Studio reload path). Utility endpoint — no group-platform routing.
		gateway.POST("/images/presign", h.OpenAIGateway.ImagesPresign)
		gateway.POST("/images/generations/async", h.AsyncImage.Submit)
		gateway.POST("/images/edits/async", h.AsyncImage.Submit)
		gateway.GET("/images/tasks/:task_id", h.AsyncImage.Get)
		registerTKOpenAICompatVideoRoutes(gateway, h)
		gateway.POST("/images/batches", h.BatchImage.Submit)
		gateway.GET("/images/batches", h.BatchImage.List)
		gateway.GET("/images/batches/models", h.BatchImage.Models)
		gateway.GET("/images/batches/:id", h.BatchImage.Get)
		gateway.GET("/images/batches/:id/items", h.BatchImage.Items)
		gateway.GET("/images/batches/:id/items/:custom_id/content", h.BatchImage.ItemContent)
		gateway.GET("/images/batches/:id/download", h.BatchImage.Download)
		gateway.POST("/images/batches/:id/cancel", h.BatchImage.Cancel)
		gateway.DELETE("/images/batches/:id", h.BatchImage.DeleteRecord)
		gateway.DELETE("/images/batches/:id/outputs", h.BatchImage.DeleteOutputs)
	}

	// Gemini 原生 API 兼容层（Gemini SDK/CLI 直连）
	gemini := r.Group("/v1beta")
	gemini.Use(bodyLimit)
	gemini.Use(clientRequestID)
	gemini.Use(trajectoryID)
	gemini.Use(qaCapture)
	gemini.Use(opsErrorLogger)
	gemini.Use(endpointNorm)
	gemini.Use(middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, cfg))
	gemini.Use(requireGroupGoogle)
	{
		gemini.GET("/models", h.Gateway.GeminiV1BetaListModels)
		gemini.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		// Gin treats ":" as a param marker, but Gemini uses "{model}:{action}" in the same segment.
		gemini.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

	// OpenAI Responses API（不带v1前缀的别名）— keep the same OpenAI-compatible
	// routing predicate as /v1/responses so newapi never drifts into a second path.
	responsesHandler := tkOpenAICompatResponsesPOST(h)
	r.POST("/responses", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, responsesHandler)
	r.POST("/responses/*subpath", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, responsesHandler)
	r.GET("/responses", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatResponsesWebSocketGET(h))
	// OpenAI Chat Completions API（不带v1前缀的别名）— auto-route based on group platform
	r.POST("/chat/completions", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatChatCompletionsPOST(h))
	r.POST("/embeddings", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatEmbeddingsHandler(h))
	r.POST("/images/generations", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatImageGenerationsHandler(h))
	r.POST("/images/edits", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatImageEditsHandler(h))
	r.GET("/models", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.Gateway.Models)
	// TK: presigned-URL re-mint for offloaded images (Studio reload), no-prefix alias.
	r.POST("/images/presign", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.OpenAIGateway.ImagesPresign)
	registerTKOpenAICompatVideoRoutesNoPrefix(r, h, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic)
	codexDirect := r.Group("/backend-api/codex")
	codexDirect.Use(bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic)
	{
		codexDirect.GET("/models", h.OpenAIGateway.CodexModels)
		codexDirect.POST("/responses", responsesHandler)
		codexDirect.POST("/responses/*subpath", responsesHandler)
		codexDirect.GET("/responses", tkOpenAICompatResponsesWebSocketGET(h))
	}

	// Antigravity 模型列表
	r.GET("/antigravity/models", bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.Gateway.AntigravityModels)

	// Antigravity 专用路由（仅使用 antigravity 账户，不混合调度）
	antigravityV1 := r.Group("/antigravity/v1")
	antigravityV1.Use(bodyLimit)
	antigravityV1.Use(clientRequestID)
	antigravityV1.Use(trajectoryID)
	antigravityV1.Use(qaCapture)
	antigravityV1.Use(opsErrorLogger)
	antigravityV1.Use(endpointNorm)
	antigravityV1.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1.Use(gin.HandlerFunc(apiKeyAuth))
	antigravityV1.Use(requireGroupAnthropic)
	{
		antigravityV1.POST("/messages", h.Gateway.Messages)
		antigravityV1.POST("/messages/count_tokens", h.Gateway.CountTokens)
		antigravityV1.GET("/models", h.Gateway.AntigravityModels)
		antigravityV1.GET("/usage", h.Gateway.Usage)
	}

	antigravityV1Beta := r.Group("/antigravity/v1beta")
	antigravityV1Beta.Use(bodyLimit)
	antigravityV1Beta.Use(clientRequestID)
	antigravityV1Beta.Use(trajectoryID)
	antigravityV1Beta.Use(qaCapture)
	antigravityV1Beta.Use(opsErrorLogger)
	antigravityV1Beta.Use(endpointNorm)
	antigravityV1Beta.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1Beta.Use(middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, cfg))
	antigravityV1Beta.Use(requireGroupGoogle)
	{
		antigravityV1Beta.GET("/models", h.Gateway.GeminiV1BetaListModels)
		antigravityV1Beta.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		antigravityV1Beta.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

}

// getGroupPlatform extracts the group platform from the API Key stored in context.
func getGroupPlatform(c *gin.Context) string {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		return ""
	}
	return apiKey.Group.Platform
}
