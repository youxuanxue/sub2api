package routes

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

		// xAI Voice APIs (Grok platform only): HTTP TTS/STT + Realtime WS.
		// Not part of the creation-center product surface — gateway relay only.
		voiceHandler := func(endpoint string) gin.HandlerFunc {
			return func(c *gin.Context) {
				if getGroupPlatform(c) != service.PlatformGrok {
					service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
					c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Voice API is not supported for this platform"}})
					return
				}
				h.OpenAIGateway.GrokVoice(c, endpoint)
			}
		}
		gatewayRoutes.Register(http.MethodPost, "/tts", SyncInference, voiceHandler("tts"))
		gatewayRoutes.Register(http.MethodPost, "/stt", SyncInference, voiceHandler("stt"))
		gatewayRoutes.Register(http.MethodPost, "/custom-voices", AsyncSubmission, voiceHandler("custom-voices"))
		customVoicePathHandler := func(c *gin.Context) {
			if getGroupPlatform(c) != service.PlatformGrok {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Voice API is not supported for this platform"}})
				return
			}
			h.OpenAIGateway.GrokVoice(c, grokCustomVoiceEndpoint(c))
		}
		gatewayRoutes.Register(http.MethodGet, "/custom-voices", Excluded("voice_management"), voiceHandler("custom-voices"))
		gatewayRoutes.Register(http.MethodGet, "/custom-voices/:voice_id/audio", Excluded("content_fetch"), customVoicePathHandler)
		gatewayRoutes.Register(http.MethodGet, "/custom-voices/:voice_id", Excluded("voice_management"), customVoicePathHandler)
		gatewayRoutes.Register(http.MethodPatch, "/custom-voices/:voice_id", Excluded("voice_management"), customVoicePathHandler)
		gatewayRoutes.Register(http.MethodDelete, "/custom-voices/:voice_id", Excluded("voice_management"), customVoicePathHandler)
		gatewayRoutes.Register(http.MethodGet, "/realtime", WebSocketTurn, func(c *gin.Context) {
			if getGroupPlatform(c) != service.PlatformGrok {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Realtime API is not supported for this platform"}})
				return
			}
			h.OpenAIGateway.GrokRealtime(c)
		})
		gatewayRoutes.Register(http.MethodPost, "/web_search", SyncInference, func(c *gin.Context) {
			if getGroupPlatform(c) != service.PlatformGrok {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Web Search API is not supported for this platform"}})
				return
			}
			h.Gateway.WebSearch(c)
		})
		gatewayRoutes.Register(http.MethodPost, "/x_search", SyncInference, func(c *gin.Context) {
			if getGroupPlatform(c) != service.PlatformGrok {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "X Search API is not supported for this platform"}})
				return
			}
			h.Gateway.XSearch(c)
		})
	}

	// OpenRouter provider catalog (seller surface). Inference still uses /v1/chat/completions.
	openrouterProvider := r.Group("/openrouter/v1")
	openrouterProvider.Use(bodyLimit)
	openrouterProvider.Use(clientRequestID)
	openrouterProvider.Use(trajectoryID)
	openrouterProvider.Use(qaCapture)
	openrouterProvider.Use(opsErrorLogger)
	openrouterProvider.Use(endpointNorm)
	openrouterProvider.Use(gin.HandlerFunc(apiKeyAuth))
	openrouterRoutes := newTerminalRouteRegistrar(openrouterProvider, terminalOutcomeRecorder)
	{
		openrouterRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), h.Gateway.OpenRouterProviderModels)
		openrouterRoutes.Register(http.MethodPost, "/images", SyncInference, h.OpenRouterProviderImages)
		openrouterRoutes.Register(http.MethodPost, "/videos", AsyncSubmission, h.OpenRouterProviderVideoSubmit)
		openrouterRoutes.Register(http.MethodGet, "/videos/:id", Excluded("status"), h.OpenRouterProviderVideoFetch)
	}

	// Gemini 原生 API 兼容层（Gemini SDK/CLI 直连）
	gemini := r.Group("/v1beta")
	gemini.Use(bodyLimit)
	gemini.Use(clientRequestID)
	gemini.Use(trajectoryID)
	gemini.Use(qaCapture)
	gemini.Use(opsErrorLogger)
	gemini.Use(endpointNorm)
	gemini.Use(middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, settingService, cfg))
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
	rootRoutes.Register(http.MethodPost, "/responses", StreamInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, responsesHandler)
	rootRoutes.Register(http.MethodPost, "/responses/*subpath", StreamInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, guardResponsesSubpath(responsesHandler))
	rootRoutes.Register(http.MethodGet, "/responses", WebSocketTurn, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatResponsesWebSocketGET(h))
	// OpenAI Chat Completions API（不带v1前缀的别名）— auto-route based on group platform
	rootRoutes.Register(http.MethodPost, "/chat/completions", StreamInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatChatCompletionsPOST(h))
	rootRoutes.Register(http.MethodPost, "/embeddings", SyncInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatEmbeddingsHandler(h))
	rootRoutes.Register(http.MethodPost, "/images/generations", SyncInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatImageGenerationsHandler(h))
	rootRoutes.Register(http.MethodPost, "/images/edits", SyncInference, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, tkOpenAICompatImageEditsHandler(h))
	rootRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, modelsHandler)
	registerTKOpenAICompatImagePresignRoutesNoPrefix(rootRoutes, h, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic)
	registerTKOpenAICompatVideoRoutesNoPrefix(rootRoutes, h, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic)
	rootRoutes.Register(http.MethodPost, "/alpha/search", SyncInference, textBodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.OpenAIGateway.AlphaSearch)
	rootRoutes.Register(http.MethodPost, "/messages/count_tokens", Excluded("count_tokens"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, countTokensHandler)
	codexDirect := r.Group("/backend-api/codex")
	codexDirect.Use(bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic)
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
	rootRoutes.Register(http.MethodPost, "/images/generations/async", AsyncSubmission, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.AsyncImage.Submit)
	rootRoutes.Register(http.MethodPost, "/images/edits/async", AsyncSubmission, bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.AsyncImage.Submit)
	rootRoutes.Register(http.MethodGet, "/images/tasks/:task_id", Excluded("status"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.AsyncImage.Get)

	rootVoiceHandler := func(endpoint string) gin.HandlerFunc {
		return func(c *gin.Context) {
			if getGroupPlatform(c) != service.PlatformGrok {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Voice API is not supported for this platform"}})
				return
			}
			h.OpenAIGateway.GrokVoice(c, endpoint)
		}
	}
	rootRoutes.Register(http.MethodPost, "/tts", SyncInference, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootVoiceHandler("tts"))
	rootRoutes.Register(http.MethodPost, "/stt", SyncInference, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootVoiceHandler("stt"))
	rootRoutes.Register(http.MethodPost, "/custom-voices", AsyncSubmission, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootVoiceHandler("custom-voices"))
	rootCustomVoicePathHandler := func(c *gin.Context) {
		if getGroupPlatform(c) != service.PlatformGrok {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Voice API is not supported for this platform"}})
			return
		}
		h.OpenAIGateway.GrokVoice(c, grokCustomVoiceEndpoint(c))
	}
	rootRoutes.Register(http.MethodGet, "/custom-voices", Excluded("voice_management"), bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootVoiceHandler("custom-voices"))
	rootRoutes.Register(http.MethodGet, "/custom-voices/:voice_id/audio", Excluded("content_fetch"), bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootCustomVoicePathHandler)
	rootRoutes.Register(http.MethodGet, "/custom-voices/:voice_id", Excluded("voice_management"), bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootCustomVoicePathHandler)
	rootRoutes.Register(http.MethodPatch, "/custom-voices/:voice_id", Excluded("voice_management"), bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootCustomVoicePathHandler)
	rootRoutes.Register(http.MethodDelete, "/custom-voices/:voice_id", Excluded("voice_management"), bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, rootCustomVoicePathHandler)
	rootRoutes.Register(http.MethodGet, "/realtime", WebSocketTurn, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, func(c *gin.Context) {
		if getGroupPlatform(c) != service.PlatformGrok {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Realtime API is not supported for this platform"}})
			return
		}
		h.OpenAIGateway.GrokRealtime(c)
	})
	rootRoutes.Register(http.MethodPost, "/web_search", SyncInference, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, func(c *gin.Context) {
		if getGroupPlatform(c) != service.PlatformGrok {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Web Search API is not supported for this platform"}})
			return
		}
		h.Gateway.WebSearch(c)
	})
	rootRoutes.Register(http.MethodPost, "/x_search", SyncInference, bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), compositeTarget, requireGroupAnthropic, func(c *gin.Context) {
		if getGroupPlatform(c) != service.PlatformGrok {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "X Search API is not supported for this platform"}})
			return
		}
		h.Gateway.XSearch(c)
	})

	// Antigravity 模型列表
	rootRoutes.Register(http.MethodGet, "/antigravity/models", Excluded("model_catalog"), bodyLimit, clientRequestID, trajectoryID, qaCapture, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.Gateway.AntigravityModels)

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
	antigravityV1Beta.Use(requireGroupGoogle)
	antigravityV1BetaRoutes := newTerminalRouteRegistrar(antigravityV1Beta, terminalOutcomeRecorder)
	{
		antigravityV1BetaRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), h.Gateway.GeminiV1BetaListModels)
		antigravityV1BetaRoutes.Register(http.MethodGet, "/models/:model", Excluded("model_catalog"), h.Gateway.GeminiV1BetaGetModel)
		antigravityV1BetaRoutes.Register(http.MethodPost, "/models/*modelAction", StreamInference, h.Gateway.GeminiV1BetaModels)
	}

}

// getGroupPlatform extracts the group platform from the API Key stored in context.
func getGroupPlatform(c *gin.Context) string {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		return ""
	}
	if apiKey.Group.Platform == service.PlatformComposite {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
			return platform
		}
	}
	return apiKey.Group.Platform
}

func compositeTargetPlatformMiddleware(resolver *service.CompositeRouteResolver) gin.HandlerFunc {
	if resolver == nil {
		resolver = service.NewCompositeRouteResolver(nil)
	}
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
			c.Next()
			return
		}
		if c.Request == nil || c.Request.Method == http.MethodGet {
			c.Next()
			return
		}

		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status := http.StatusBadRequest
			message := "Failed to read request body"
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				status = http.StatusRequestEntityTooLarge
				message = "Request body is too large"
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
			c.Abort()
			return
		}

		model := compositeRequestModelFromBody(c.GetHeader("Content-Type"), body)
		if model != "" {
			decision, err := resolver.Resolve(c.Request.Context(), apiKey.Group.ID, model, compositeRouteEndpointForPath(c.Request.URL.Path))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "server_error", "message": "Failed to resolve composite model route"}})
				c.Abort()
				return
			}
			if decision.Matched {
				c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
				if upstreamModel := strings.TrimSpace(decision.UpstreamModel); upstreamModel != "" && upstreamModel != model && gjson.ValidBytes(body) {
					if _, modelPath := compositeJSONRequestModel(body); modelPath != "" {
						if rewritten, rewriteErr := sjson.SetBytes(body, modelPath, upstreamModel); rewriteErr == nil {
							body = rewritten
						}
					}
				}
			}
		}
		resetRequestBody(c, body)
		c.Next()
	}
}

func compositeRequestModelFromBody(contentType string, body []byte) string {
	if model, _ := compositeJSONRequestModel(body); model != "" {
		return model
	}
	return compositeMultipartModelFromBody(contentType, body)
}

func compositeJSONRequestModel(body []byte) (string, string) {
	for _, path := range []string{"model", "session.model"} {
		model := gjson.GetBytes(body, path)
		if model.Type != gjson.String {
			continue
		}
		if value := strings.TrimSpace(model.String()); value != "" {
			return value, path
		}
	}
	return "", ""
}

func compositeMultipartModelFromBody(contentType string, body []byte) string {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return ""
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			return ""
		}
		fieldName := part.FormName()
		if part.FileName() != "" || (fieldName != "model" && fieldName != "session") {
			continue
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return ""
		}
		switch fieldName {
		case "model":
			return strings.TrimSpace(string(data))
		case "session":
			if model, _ := compositeJSONRequestModel(data); model != "" {
				return model
			}
		}
	}
}

func compositeGeminiTargetPlatformMiddleware(resolver *service.CompositeRouteResolver) gin.HandlerFunc {
	if resolver == nil {
		resolver = service.NewCompositeRouteResolver(nil)
	}
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform == service.PlatformComposite {
			model := compositeGeminiModelFromParams(c)
			if model != "" {
				decision, err := resolver.Resolve(c.Request.Context(), apiKey.Group.ID, model, service.CompositeRouteEndpointGemini)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "server_error", "message": "Failed to resolve composite model route"}})
					c.Abort()
					return
				}
				if decision.Matched {
					c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
				}
			}
			if _, resolved := service.ResolvedTargetPlatformFromContext(c.Request.Context()); !resolved {
				c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), service.PlatformGemini))
			}
		}
		c.Next()
	}
}

// grokCustomVoiceEndpoint derives the upstream Voice endpoint for the
// /custom-voices/:voice_id[/audio] routes.
//
// The /audio suffix must be decided from the matched route template, not from
// the raw URL path: a voice literally named "audio" makes GET
// /custom-voices/audio match /custom-voices/:voice_id, and a raw-path suffix
// check would rewrite it to custom-voices/audio/audio — turning a profile
// lookup into an audio download.
func grokCustomVoiceEndpoint(c *gin.Context) string {
	endpoint := "custom-voices/" + c.Param("voice_id")
	if strings.HasSuffix(c.FullPath(), "/:voice_id/audio") {
		endpoint += "/audio"
	}
	return endpoint
}

func compositeGeminiModelFromParams(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if model := strings.TrimSpace(c.Param("model")); model != "" {
		return model
	}
	modelAction := strings.TrimPrefix(strings.TrimSpace(c.Param("modelAction")), "/")
	if modelAction == "" {
		return ""
	}
	if idx := strings.LastIndex(modelAction, ":"); idx >= 0 {
		return strings.TrimSpace(modelAction[:idx])
	}
	return modelAction
}

func resetRequestBody(c *gin.Context, body []byte) {
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func compositeRouteEndpointForPath(path string) string {
	switch {
	case strings.Contains(path, "/messages/count_tokens"):
		return service.CompositeRouteEndpointCountTokens
	case strings.Contains(path, "/messages"):
		return service.CompositeRouteEndpointMessages
	case strings.Contains(path, "/responses"),
		strings.Contains(path, "/alpha/search"),
		strings.Contains(path, "/realtime/calls"),
		strings.HasSuffix(strings.TrimRight(path, "/"), "/live"):
		return service.CompositeRouteEndpointResponses
	case strings.Contains(path, "/chat/completions"):
		return service.CompositeRouteEndpointChatCompletions
	case strings.Contains(path, "/embeddings"):
		return service.CompositeRouteEndpointEmbeddings
	case strings.Contains(path, "/images/"):
		return service.CompositeRouteEndpointImages
	case strings.Contains(path, "/v1beta/"):
		return service.CompositeRouteEndpointGemini
	default:
		return service.CompositeRouteEndpointAny
	}
}
