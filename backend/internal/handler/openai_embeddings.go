package handler

import "github.com/gin-gonic/gin"

// Embeddings handles POST /v1/embeddings for OpenAI-platform API keys.
func (h *OpenAIGatewayHandler) Embeddings(c *gin.Context) {
	h.embeddings(c)
}
