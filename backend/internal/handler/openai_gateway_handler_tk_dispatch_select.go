package handler

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// tkOpenAIDispatchSelectionFallbackModel returns the group-level
// messages-dispatch mapped model when the first account selection failed
// solely because the client-requested Claude name had no eligible account
// left in the OpenAI-compat pool.
//
// Prod 2026-08-20 (user_id=6, group GPT专线, model=claude-opus-4-8):
// cloudwise (native Claude relay) went status=error on 402 insufficient
// balance; tokensea advertises claude-opus-4-8 but /v1/messages returns
// "not implemented". Selection kept using the client model name, so the
// three native OpenAI accounts that can serve opus_mapped_model=gpt-5.6-sol
// were counted as model_unsupported and the user saw routing 429s. The
// mapped id is already applied at forward time; this helper makes selection
// use the same fallback after the native-Claude pool is empty.
func tkOpenAIDispatchSelectionFallbackModel(mappedModel, primaryModel string, selectErr error) (string, bool) {
	if selectErr == nil {
		return "", false
	}
	mappedModel = strings.TrimSpace(mappedModel)
	primaryModel = strings.TrimSpace(primaryModel)
	if mappedModel == "" || mappedModel == primaryModel {
		return "", false
	}
	if !tkOpenAIDispatchSelectionAllowsFallback(selectErr) {
		return "", false
	}
	return mappedModel, true
}

func tkOpenAIDispatchSelectionAllowsFallback(err error) bool {
	if err == nil {
		return false
	}
	// Compact-only exhaustion must not fall back to a non-compact GPT account.
	if errors.Is(err, service.ErrNoAvailableCompactAccounts) {
		return false
	}
	if errors.Is(err, service.ErrUnsupportedModel) {
		return true
	}
	return isOpsNoAvailableAccountError(err)
}

type tkSchedulerCapabilitySelectArgs struct {
	GroupID                    *int64
	PreviousResponseID         string
	SessionHash                string
	FailedAccountIDs           map[int64]struct{}
	Transport                  service.OpenAIUpstreamTransport
	Capability                 service.OpenAIEndpointCapability
	RequireCompact             bool
	PreviousResponseCanMove    bool
	ExcludeImageIntentAccounts bool
	Platform                   string
}

func (h *OpenAIGatewayHandler) tkSelectAccountWithSchedulerDispatchFallback(
	ctx context.Context,
	reqLog *zap.Logger,
	mappedModel string,
	routingModel string,
	logEvent string,
	args tkSchedulerCapabilitySelectArgs,
) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, string, error) {
	selectFor := func(model string) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
		return h.gatewayService.SelectAccountWithSchedulerForCapability(
			ctx,
			args.GroupID,
			args.PreviousResponseID,
			args.SessionHash,
			model,
			args.FailedAccountIDs,
			args.Transport,
			args.Capability,
			args.RequireCompact,
			args.PreviousResponseCanMove,
			args.ExcludeImageIntentAccounts,
			args.Platform,
		)
	}
	selection, scheduleDecision, err := selectFor(routingModel)
	effectiveRoutingModel := routingModel
	if fallbackModel, ok := tkOpenAIDispatchSelectionFallbackModel(mappedModel, routingModel, err); ok {
		reqLog.Info(logEvent,
			zap.String("from_model", routingModel),
			zap.String("to_model", fallbackModel),
			zap.Error(err),
		)
		selection, scheduleDecision, err = selectFor(fallbackModel)
		if err == nil {
			effectiveRoutingModel = fallbackModel
		}
	}
	return selection, scheduleDecision, effectiveRoutingModel, err
}

func (h *OpenAIGatewayHandler) tkSelectAccountForTokenCountWithDispatchFallback(
	ctx context.Context,
	reqLog *zap.Logger,
	mappedModel string,
	routingModel string,
	logEvent string,
	selectFn func(model string) (*service.Account, error),
) (*service.Account, string, error) {
	account, err := selectFn(routingModel)
	effectiveRoutingModel := routingModel
	if fallbackModel, ok := tkOpenAIDispatchSelectionFallbackModel(mappedModel, routingModel, err); ok {
		reqLog.Info(logEvent,
			zap.String("from_model", routingModel),
			zap.String("to_model", fallbackModel),
			zap.Error(err),
		)
		account, err = selectFn(fallbackModel)
		if err == nil {
			effectiveRoutingModel = fallbackModel
		}
	}
	return account, effectiveRoutingModel, err
}
