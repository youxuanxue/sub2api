package handler

import (
	"fmt"
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAIImagesUnknownModelNotFound upgrades a capacity-style Images selection
// failure to OpenAI-shaped 404 when the requested gpt-image-* id has no pricing
// registry row. Empty model_mapping otherwise treats unknown ids as schedulable
// and surfaces 503, which SandBase protocol-error-model rejects.
func openAIImagesUnknownModelNotFound(
	c *gin.Context,
	gateway *service.OpenAIGatewayService,
	apiKey *service.APIKey,
	model string,
	size string,
	status int,
	errType string,
	message string,
) (int, string, string) {
	if status == service.TkUnsupportedModelStatus {
		return status, errType, message
	}
	if gateway == nil || !service.IsGPTImageGenerationModel(model) {
		return status, errType, message
	}
	var group *service.Group
	if apiKey != nil {
		group = apiKey.Group
	}
	if !gateway.TkImageModelUnpriced(model, group, size) || !gateway.TkImageModelRegistryMissing(model) {
		return status, errType, message
	}
	return service.TkUnsupportedModelSelectionResponseWithMessage(c, fmt.Sprintf("The model '%s' does not exist", model))
}
