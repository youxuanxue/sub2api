package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// tkWriteDeprecatedAnthropicModelResponse writes the Anthropic-shape HTTP 400
// migration envelope when reqModel is on the retired list. Returns true when
// handled (response committed).
func tkWriteDeprecatedAnthropicModelResponse(c *gin.Context, reqModel string, reqLog *zap.Logger, logEvent string) bool {
	_, replacement, ok := service.TkLookupDeprecatedAnthropicModel(reqModel)
	if !ok {
		return false
	}
	if reqLog != nil && logEvent != "" {
		reqLog.Warn(logEvent, zap.String("model", reqModel))
	}
	markOpsClientRequestRejected(c)
	service.TkWriteAnthropicDeprecatedModelError(c, reqModel, replacement)
	return true
}

// tkWriteDeprecatedAnthropicModelAtIngress rejects retired Anthropic model IDs
// before account selection so sunset requests never enter routing/scheduling or
// surface as empty-pool routing 429.
func (h *GatewayHandler) tkWriteDeprecatedAnthropicModelAtIngress(c *gin.Context, reqModel string, reqLog *zap.Logger) bool {
	return tkWriteDeprecatedAnthropicModelResponse(c, reqModel, reqLog, "gateway.deprecated_model_ingress_reject")
}

// tkWriteDeprecatedAnthropicModelIfApplicable maps account-selection failures for
// retired models to the Anthropic-shape HTTP 400 migration envelope and returns
// true (handled). Covers ErrDeprecatedAnthropicModel from tkWrapSelectionFailure /
// TkSelectionNoAvailableAccountsError, and load-batch ErrNoAvailableAccounts when
// reqModel is still a sunset id.
//
// Why this exists: the Forward-path deprecated gate (gateway_anthropic_deprecated_model_tk.go)
// only runs after an account is selected. When every whitelist account rejects a
// retired snapshot and the pool also has unschedulable candidates, tkWrapSelectionFailure
// used to fall back to ErrNoAvailableAccounts → routing 429 + Retry-After even though
// the client can never succeed without migrating off the sunset id.
func (h *GatewayHandler) tkWriteDeprecatedAnthropicModelIfApplicable(c *gin.Context, err error, reqModel string, reqLog *zap.Logger) bool {
	if errors.Is(err, service.ErrDeprecatedAnthropicModel) {
		return tkWriteDeprecatedAnthropicModelResponse(c, reqModel, reqLog, "gateway.select_account_deprecated_model")
	}
	if isOpsNoAvailableAccountError(err) {
		return tkWriteDeprecatedAnthropicModelResponse(c, reqModel, reqLog, "gateway.select_account_deprecated_model")
	}
	return false
}
