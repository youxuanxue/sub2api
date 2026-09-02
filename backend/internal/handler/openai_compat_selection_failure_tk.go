package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// openAICompatFirstAttemptSelectionFailure is the SSOT for first-attempt account-
// selection failures on OpenAI-compat routes (openai / newapi / grok pools).
// routingModel is the model account selection compared; displayModel is the
// client-facing model name in error bodies.
//
// Order: unsupported model name → 400; persistent mapping gap → 404 model_not_found
// (via group platform diagnosis); empty pool / scheduler faults → 429 or 503.
func openAICompatFirstAttemptSelectionFailure(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
	err error,
) (status int, errType string, message string) {
	if err != nil && errors.Is(err, service.ErrUnsupportedModel) {
		return tkSelectFailureStatusMessage(c, err, displayModel)
	}
	cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, diag, apiKey, routingModel, displayModel)
	if cls.ModelNotFound {
		return cls.Status, cls.ErrType, cls.Message
	}
	if err != nil {
		markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		return tkSelectFailureStatusMessage(c, err, displayModel)
	}
	markOpsRoutingCapacityLimited(c)
	return cls.Status, cls.ErrType, cls.Message
}
