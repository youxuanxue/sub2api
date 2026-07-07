package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// tkWriteDeprecatedAnthropicModelIfApplicable maps a service.ErrDeprecatedAnthropicModel
// account-selection failure to the Anthropic-shape HTTP 400 migration envelope and
// returns true (handled). For any other error it returns false so the caller falls
// through to unsupported-model / empty-pool handling.
//
// Why this exists: the Forward-path deprecated gate (gateway_anthropic_deprecated_model_tk.go)
// only runs after an account is selected. When every whitelist account rejects a
// retired snapshot and the pool also has unschedulable candidates, tkWrapSelectionFailure
// used to fall back to ErrNoAvailableAccounts → routing 429 + Retry-After even though
// the client can never succeed without migrating off the sunset id.
func (h *GatewayHandler) tkWriteDeprecatedAnthropicModelIfApplicable(c *gin.Context, err error, reqModel string, reqLog *zap.Logger) bool {
	if !errors.Is(err, service.ErrDeprecatedAnthropicModel) {
		return false
	}
	if reqLog != nil {
		reqLog.Warn("gateway.select_account_deprecated_model",
			zap.String("model", reqModel),
			zap.Error(err),
		)
	}
	markOpsClientRequestRejected(c)
	if _, replacement, ok := service.TkLookupDeprecatedAnthropicModel(reqModel); ok {
		service.TkWriteAnthropicDeprecatedModelError(c, reqModel, replacement)
	}
	return true
}
