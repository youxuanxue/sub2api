package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func registerTKGrokVoiceRoutesV1(gatewayRoutes terminalRouteRegistrar, h *handler.Handlers) {
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

func registerTKGrokVoiceRoutesRoot(
	rootRoutes terminalRouteRegistrar,
	h *handler.Handlers,
	bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuth, compositeTarget, requireGroupAnthropic gin.HandlerFunc,
) {
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
}
