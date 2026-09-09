package routes

import (
	"net/http"

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
	compositeResolver *service.CompositeRouteResolver,
	terminalOutcomeRecorder *service.TerminalOutcomeRecorder,
	cfg *config.Config,
) {
	bodyLimit := middleware.RequestBodyLimit(cfg.Gateway.MaxBodySize)
	textBodyLimit := middleware.RequestBodyLimit(cfg.Gateway.TextMaxBodySize)
	clientRequestID := middleware.ClientRequestID()
	trajectoryID := middleware.TrajectoryID()
	opsErrorLogger := handler.OpsErrorLoggerMiddleware(opsService)
	endpointNorm := handler.InboundEndpointMiddleware()
	qaCapture := gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if h != nil && h.QACapture != nil {
		qaCapture = h.QACapture.Middleware()
	}
	groupModelAllowlist := middleware.GroupModelAllowlist()
	compositeTarget := compositeTargetPlatformMiddleware(compositeResolver)
	compositeGeminiTarget := compositeGeminiTargetPlatformMiddleware(compositeResolver)

	// 未分组 Key 拦截中间件（按协议格式区分错误响应）
	requireGroupAnthropic := middleware.RequireGroupAssignment(settingService, middleware.AnthropicErrorWriter)
	requireGroupGoogle := middleware.RequireGroupAssignment(settingService, middleware.GoogleErrorWriter)

	countTokensHandler := tkOpenAICompatCountTokensPOST(h)
	modelsHandler := tkModelsHandler(h)
	// /responses/*subpath：入口拒掉不可转发子路径（见 service.IsForwardableOpenAIResponsesRequestPath）。
	guardResponsesSubpath := tkGuardResponsesSubpath(h)
	responsesHandler := tkOpenAICompatResponsesPOST(h)

	// API网关（Claude API兼容）
	gateway := r.Group("/v1")
	gateway.Use(bodyLimit)
	gateway.Use(clientRequestID)
	gateway.Use(trajectoryID)
	gateway.Use(qaCapture)
	gateway.Use(opsErrorLogger)
	gateway.Use(endpointNorm)
	gateway.Use(gin.HandlerFunc(apiKeyAuth))
	gatewayRoutes := newTerminalRouteRegistrar(gateway, terminalOutcomeRecorder)
	gatewayRoutes.Register(http.MethodGet, "/sub2api/billing", Excluded("billing"), h.Gateway.KeyBillingInfo)
	gateway.Use(groupModelAllowlist)
	gateway.Use(compositeTarget)
	gateway.Use(requireGroupAnthropic)
	{
		// /v1/messages: auto-route based on group platform
		gatewayRoutes.Register(http.MethodPost, "/messages", StreamInference, tkOpenAICompatMessagesPOST(h))
		// /v1/messages/count_tokens: OpenAI bridges upstream, Grok estimates
		// locally, and Anthropic-compatible platforms retain their existing path.
		gatewayRoutes.Register(http.MethodPost, "/messages/count_tokens", Excluded("count_tokens"), countTokensHandler)
		// Codex CLI / Codex app refresh their model picker from the provider's
		// /models endpoint with a client_version query and expect the ChatGPT
		// Codex manifest format; other clients keep the OpenAI-style list.
		gatewayRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), modelsHandler)
		gatewayRoutes.Register(http.MethodGet, "/usage", Excluded("usage"), h.Gateway.Usage)
		gatewayRoutes.Register(http.MethodPost, "/live", AsyncSubmission, h.OpenAIGateway.Live)
		gatewayRoutes.Register(http.MethodGet, "/live/:call_id", Excluded("status"), h.OpenAIGateway.LiveSideband)
		// OpenAI Responses API: auto-route based on group platform
		gatewayRoutes.Register(http.MethodPost, "/responses", StreamInference, responsesHandler)
		gatewayRoutes.Register(http.MethodPost, "/responses/*subpath", StreamInference, guardResponsesSubpath(responsesHandler))
		gatewayRoutes.Register(http.MethodGet, "/responses", WebSocketTurn, tkOpenAICompatResponsesWebSocketGET(h))
		// OpenAI Chat Completions API: auto-route based on group platform
		gatewayRoutes.Register(http.MethodPost, "/chat/completions", StreamInference, tkOpenAICompatChatCompletionsPOST(h))
		gatewayRoutes.Register(http.MethodPost, "/embeddings", SyncInference, tkOpenAICompatEmbeddingsHandler(h))
		gatewayRoutes.Register(http.MethodPost, "/images/generations", SyncInference, tkOpenAICompatImageGenerationsHandler(h))
		gatewayRoutes.Register(http.MethodPost, "/images/edits", SyncInference, tkOpenAICompatImageEditsHandler(h))
		gatewayRoutes.Register(http.MethodPost, "/audio/speech", SyncInference, tkOpenAICompatAudioSpeechHandler(h))
		registerTKOpenAICompatImagePresignRoutes(gatewayRoutes, h)
		registerTKOpenAICompatVideoRoutes(gatewayRoutes, h)
		gatewayRoutes.Register(http.MethodPost, "/alpha/search", SyncInference, textBodyLimit, h.OpenAIGateway.AlphaSearch)
		gatewayRoutes.Register(http.MethodPost, "/images/generations/async", AsyncSubmission, h.AsyncImage.Submit)
		gatewayRoutes.Register(http.MethodPost, "/images/edits/async", AsyncSubmission, h.AsyncImage.Submit)
		gatewayRoutes.Register(http.MethodGet, "/images/tasks/:task_id", Excluded("status"), h.AsyncImage.Get)
		if h.BatchImage != nil {
			gatewayRoutes.Register(http.MethodPost, "/images/batches", AsyncSubmission, h.BatchImage.Submit)
			gatewayRoutes.Register(http.MethodGet, "/images/batches", Excluded("batch_status"), h.BatchImage.List)
			gatewayRoutes.Register(http.MethodGet, "/images/batches/models", Excluded("model_catalog"), h.BatchImage.Models)
			gatewayRoutes.Register(http.MethodGet, "/images/batches/:id", Excluded("batch_status"), h.BatchImage.Get)
			gatewayRoutes.Register(http.MethodGet, "/images/batches/:id/items", Excluded("batch_status"), h.BatchImage.Items)
			gatewayRoutes.Register(http.MethodGet, "/images/batches/:id/items/:custom_id/content", Excluded("content_fetch"), h.BatchImage.ItemContent)
			gatewayRoutes.Register(http.MethodGet, "/images/batches/:id/download", Excluded("content_fetch"), h.BatchImage.Download)
			gatewayRoutes.Register(http.MethodPost, "/images/batches/:id/cancel", Excluded("batch_control"), h.BatchImage.Cancel)
			gatewayRoutes.Register(http.MethodDelete, "/images/batches/:id", Excluded("batch_control"), h.BatchImage.DeleteRecord)
			gatewayRoutes.Register(http.MethodDelete, "/images/batches/:id/outputs", Excluded("batch_control"), h.BatchImage.DeleteOutputs)
		}

		// TK: Grok voice/realtime/search — see gateway_tk_grok_voice_routes.go
		registerTKGrokVoiceRoutesV1(gatewayRoutes, h)
	}

	// TK: OpenRouter seller catalog — see gateway_tk_openrouter_routes.go
	registerTKOpenRouterProviderRoutes(r, h, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), terminalOutcomeRecorder)

	// Gemini 原生 API 兼容层（Gemini SDK/CLI 直连）
	gemini := r.Group("/v1beta")
	gemini.Use(bodyLimit)
	gemini.Use(clientRequestID)
	gemini.Use(trajectoryID)
	gemini.Use(qaCapture)
	gemini.Use(opsErrorLogger)
	gemini.Use(endpointNorm)
	gemini.Use(middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, settingService, cfg))
	gemini.Use(groupModelAllowlist)
	gemini.Use(compositeGeminiTarget)
	gemini.Use(requireGroupGoogle)
	geminiRoutes := newTerminalRouteRegistrar(gemini, terminalOutcomeRecorder)
	{
		geminiRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), h.Gateway.GeminiV1BetaListModels)
		geminiRoutes.Register(http.MethodGet, "/models/:model", Excluded("model_catalog"), h.Gateway.GeminiV1BetaGetModel)
		// Gin treats ":" as a param marker, but Gemini uses "{model}:{action}" in the same segment.
		geminiRoutes.Register(http.MethodPost, "/models/*modelAction", StreamInference, h.Gateway.GeminiV1BetaModels)
	}

	// OpenAI Responses API（不带v1前缀的别名）— keep the same OpenAI-compatible
	// routing predicate as /v1/responses so newapi never drifts into a second path.
	rootRoutes := newTerminalRouteRegistrar(r, terminalOutcomeRecorder)
	rootRoutes.Register(http.MethodPost, "/responses", StreamInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, responsesHandler)
	rootRoutes.Register(http.MethodPost, "/responses/*subpath", StreamInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, guardResponsesSubpath(responsesHandler))
	rootRoutes.Register(http.MethodGet, "/responses", WebSocketTurn, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, tkOpenAICompatResponsesWebSocketGET(h))
	// OpenAI Chat Completions API（不带v1前缀的别名）— auto-route based on group platform
	rootRoutes.Register(http.MethodPost, "/chat/completions", StreamInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, tkOpenAICompatChatCompletionsPOST(h))
	rootRoutes.Register(http.MethodPost, "/embeddings", SyncInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, tkOpenAICompatEmbeddingsHandler(h))
	rootRoutes.Register(http.MethodPost, "/images/generations", SyncInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, tkOpenAICompatImageGenerationsHandler(h))
	rootRoutes.Register(http.MethodPost, "/images/edits", SyncInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, tkOpenAICompatImageEditsHandler(h))
	rootRoutes.Register(http.MethodPost, "/audio/speech", SyncInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, tkOpenAICompatAudioSpeechHandler(h))
	rootRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, modelsHandler)
	registerTKOpenAICompatImagePresignRoutesNoPrefix(rootRoutes, h, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic)
	registerTKOpenAICompatVideoRoutesNoPrefix(rootRoutes, h, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic)
	rootRoutes.Register(http.MethodPost, "/alpha/search", SyncInference, textBodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, h.OpenAIGateway.AlphaSearch)
	rootRoutes.Register(http.MethodPost, "/messages/count_tokens", Excluded("count_tokens"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, countTokensHandler)
	codexDirect := r.Group("/backend-api/codex")
	codexDirect.Use(bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic)
	codexRoutes := newTerminalRouteRegistrar(codexDirect, terminalOutcomeRecorder)
	{
		codexRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), h.OpenAIGateway.CodexModels)
		codexRoutes.Register(http.MethodPost, "/realtime/calls", AsyncSubmission, h.OpenAIGateway.Live)
		codexRoutes.Register(http.MethodGet, "/:call_id", Excluded("status"), h.OpenAIGateway.LiveSideband)
		codexRoutes.Register(http.MethodPost, "/responses", StreamInference, responsesHandler)
		codexRoutes.Register(http.MethodPost, "/responses/*subpath", StreamInference, guardResponsesSubpath(responsesHandler))
		codexRoutes.Register(http.MethodPost, "/alpha/search", SyncInference, textBodyLimit, h.OpenAIGateway.AlphaSearch)
		codexRoutes.Register(http.MethodGet, "/responses", WebSocketTurn, tkOpenAICompatResponsesWebSocketGET(h))
	}
	rootRoutes.Register(http.MethodPost, "/images/generations/async", AsyncSubmission, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, h.AsyncImage.Submit)
	rootRoutes.Register(http.MethodPost, "/images/edits/async", AsyncSubmission, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, h.AsyncImage.Submit)
	rootRoutes.Register(http.MethodGet, "/images/tasks/:task_id", Excluded("status"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, h.AsyncImage.Get)

	// TK: Grok voice/realtime/search (no /v1 prefix) — see gateway_tk_grok_voice_routes.go
	registerTKGrokVoiceRoutesRoot(rootRoutes, h, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic)

	// Antigravity 模型列表
	rootRoutes.Register(http.MethodGet, "/antigravity/models", Excluded("model_catalog"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, requireGroupAnthropic, h.Gateway.AntigravityModels)

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
	antigravityV1.Use(groupModelAllowlist)
	antigravityV1.Use(requireGroupAnthropic)
	antigravityV1Routes := newTerminalRouteRegistrar(antigravityV1, terminalOutcomeRecorder)
	{
		antigravityV1Routes.Register(http.MethodPost, "/messages", StreamInference, h.Gateway.Messages)
		antigravityV1Routes.Register(http.MethodPost, "/messages/count_tokens", Excluded("count_tokens"), h.Gateway.CountTokens)
		antigravityV1Routes.Register(http.MethodGet, "/models", Excluded("model_catalog"), h.Gateway.AntigravityModels)
		antigravityV1Routes.Register(http.MethodGet, "/usage", Excluded("usage"), h.Gateway.Usage)
	}

	antigravityV1Beta := r.Group("/antigravity/v1beta")
	antigravityV1Beta.Use(bodyLimit)
	antigravityV1Beta.Use(clientRequestID)
	antigravityV1Beta.Use(trajectoryID)
	antigravityV1Beta.Use(qaCapture)
	antigravityV1Beta.Use(opsErrorLogger)
	antigravityV1Beta.Use(endpointNorm)
	antigravityV1Beta.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1Beta.Use(middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, settingService, cfg))
	antigravityV1Beta.Use(groupModelAllowlist)
	antigravityV1Beta.Use(requireGroupGoogle)
	antigravityV1BetaRoutes := newTerminalRouteRegistrar(antigravityV1Beta, terminalOutcomeRecorder)
	{
		antigravityV1BetaRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), h.Gateway.GeminiV1BetaListModels)
		antigravityV1BetaRoutes.Register(http.MethodGet, "/models/:model", Excluded("model_catalog"), h.Gateway.GeminiV1BetaGetModel)
		antigravityV1BetaRoutes.Register(http.MethodPost, "/models/*modelAction", StreamInference, h.Gateway.GeminiV1BetaModels)
	}

}
