package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) rejectDeprecatedOpenAICompatModel(c *gin.Context, apiKey *service.APIKey, model string, anthropicShape bool) bool {
	if openAICompatibleRequestPlatform(c.Request.Context(), apiKey) != service.PlatformOpenAI {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	_, replacement, ok := service.TkLookupDeprecatedOpenAIModel(model)
	if !ok {
		return false
	}
	markOpsClientRequestRejected(c)
	message := service.TkBuildDeprecatedOpenAIModelMessage(model, replacement)
	if anthropicShape {
		h.anthropicErrorResponse(c, http.StatusBadRequest, service.TkDeprecatedOpenAIErrorType, message)
		return true
	}
	service.TkWriteOpenAIDeprecatedModelError(c, model, replacement)
	return true
}

func (h *OpenAIGatewayHandler) rejectDeprecatedOpenAICompatMappedModel(c *gin.Context, apiKey *service.APIKey, model string, anthropicShape bool) bool {
	if strings.TrimSpace(model) == "" {
		return false
	}
	return h.rejectDeprecatedOpenAICompatModel(c, apiKey, model, anthropicShape)
}
