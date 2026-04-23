package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// isOpenAICompatPlatform delegates to the canonical service-layer helper so
// that adding a sixth compat platform only requires updating
// service.OpenAICompatPlatforms(). Kept as a thin local wrapper to keep the
// route-table call sites concise.
func isOpenAICompatPlatform(platform string) bool {
	return service.IsOpenAICompatPlatform(platform)
}

func tkOpenAICompatMessagesPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.Messages(c)
			return
		}
		h.Gateway.Messages(c)
	}
}

func tkOpenAICompatCountTokensPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Token counting is not supported for this platform",
				},
			})
			return
		}
		h.Gateway.CountTokens(c)
	}
}

func tkOpenAICompatResponsesPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.Responses(c)
			return
		}
		h.Gateway.Responses(c)
	}
}

func tkOpenAICompatChatCompletionsPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.ChatCompletions(c)
			return
		}
		h.Gateway.ChatCompletions(c)
	}
}

// tkOpenAICompatEmbeddingsHandler routes POST /embeddings for OpenAI-compat (incl. newapi) platform groups only.
func tkOpenAICompatEmbeddingsHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The embeddings API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.Embeddings(c)
	}
}

// tkOpenAICompatImageGenerationsHandler routes POST /images/generations for OpenAI-compat platform groups only.
func tkOpenAICompatImageGenerationsHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The images API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.ImageGenerations(c)
	}
}

// tkOpenAICompatVideoSubmitHandler routes POST /video/generations and POST /videos
// for OpenAI-compat (incl. newapi) platform groups only. The async video task
// pipeline is currently bridge-only (no native sub2api implementation exists),
// so non-compat groups always 404 here regardless of the underlying platform's
// capabilities.
func tkOpenAICompatVideoSubmitHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The video generation API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.VideoSubmit(c)
	}
}

// tkOpenAICompatVideoFetchHandler routes GET /video/generations/:task_id and
// GET /videos/:task_id for OpenAI-compat platform groups. We deliberately
// allow the fetch to skip the platform check whenever the registry has the
// task — a client that submitted under a newapi group but later switches to
// an openai group should still be able to poll a task they already created
// (the registry pins routing to the original account anyway). The 404 only
// fires when the platform is unknown AND the registry has no record.
func tkOpenAICompatVideoFetchHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The video generation API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.VideoFetch(c)
	}
}
