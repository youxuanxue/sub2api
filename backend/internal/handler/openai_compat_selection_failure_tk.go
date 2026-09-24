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
// Concrete candidate verdicts bypass legacy group diagnosis. For ambiguous
// empty selections, a persistent mapping gap returns 404 model_not_found;
// otherwise empty pools / scheduler faults return 429 or 503.
func openAICompatFirstAttemptSelectionFailure(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
	err error,
) (status int, errType string, message string) {
	// Candidate selection already established support across authorized pools;
	// the API key's billing group cannot reclassify that verdict as a mapping gap.
	if errors.Is(err, service.ErrUniversalCapacityUnavailable) {
		markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		return tkSelectFailureStatusMessage(c, err, displayModel)
	}
	// Persistent mapping diagnosis only explains an empty selection. It must
	// never overwrite a concrete scheduler fault (DB/context/etc.) with a 404.
	if err != nil && !isOpsNoAvailableAccountError(err) {
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
	return tkNoAvailableAccounts(c), "api_error", "No available accounts"
}
