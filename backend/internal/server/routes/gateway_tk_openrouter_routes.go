package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// registerTKOpenRouterProviderRoutes mounts the OpenRouter seller catalog surface.
// Inference still uses /v1/chat/completions.
func registerTKOpenRouterProviderRoutes(
	r gin.IRouter,
	h *handler.Handlers,
	bodyLimit gin.HandlerFunc,
	clientRequestID gin.HandlerFunc,
	trajectoryID gin.HandlerFunc,
	qaCapture gin.HandlerFunc,
	opsErrorLogger gin.HandlerFunc,
	endpointNorm gin.HandlerFunc,
	apiKeyAuth gin.HandlerFunc,
	terminalOutcomeRecorder service.TerminalOutcomeSink,
) {
	openrouterProvider := r.Group("/openrouter/v1")
	openrouterProvider.Use(bodyLimit)
	openrouterProvider.Use(clientRequestID)
	openrouterProvider.Use(trajectoryID)
	openrouterProvider.Use(qaCapture)
	openrouterProvider.Use(opsErrorLogger)
	openrouterProvider.Use(endpointNorm)
	openrouterProvider.Use(apiKeyAuth)
	openrouterRoutes := newTerminalRouteRegistrar(openrouterProvider, terminalOutcomeRecorder)
	{
		openrouterRoutes.Register(http.MethodGet, "/models", Excluded("model_catalog"), h.Gateway.OpenRouterProviderModels)
		openrouterRoutes.Register(http.MethodPost, "/images", SyncInference, h.OpenRouterProviderImages)
		openrouterRoutes.Register(http.MethodPost, "/videos", AsyncSubmission, h.OpenRouterProviderVideoSubmit)
		openrouterRoutes.Register(http.MethodGet, "/videos/:id", Excluded("status"), h.OpenRouterProviderVideoFetch)
	}
}
