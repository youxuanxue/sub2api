package service

import (
	"context"
	"fmt"
	"strings"
)

// openAICompatErrorPlatformLabel returns the platform identifier to embed in
// "no available accounts" error messages produced by the OpenAI-compat
// scheduler.
//
// Background (docs/bugs/2026-04-23-newapi-fifth-platform-audit.md, P1-2):
// the OpenAI-compat scheduling pool now carries both `openai` and `newapi`
// accounts (see docs/approved/newapi-as-fifth-platform.md). Hard-coded
// "no available OpenAI accounts" error text caused operator confusion when
// the failing group was actually `newapi`. We surface req.GroupPlatform
// verbatim, falling back to PlatformOpenAI for the legacy "no group / empty
// platform" case so existing tests / log greps that expect "openai" continue
// to work for openai-shaped requests.
//
// Kept in a TK-only companion file so future upstream merges of
// openai_account_scheduler.go do not collide on this branding choice.
func openAICompatErrorPlatformLabel(groupPlatform string) string {
	if groupPlatform == "" {
		return PlatformOpenAI
	}
	return groupPlatform
}

// openAICompatNoCandidateError is the SINGLE exit for BOTH OpenAI-compat selection
// paths whose schedulable pool was emptied by the candidate filter:
//   - the load-balance scheduler (defaultOpenAIAccountScheduler.selectByLoadBalance,
//     len(filtered)==0); and
//   - the priority/LRU path (OpenAIGatewayService.selectAccountForModelWithExclusions,
//     selectBestAccount → nil), which count_tokens and the sticky/legacy callers use.
//
// It mirrors the anthropic path's tkWrapSelectionFailure
// (gateway_service_tk_unsupported_model.go): when EVERY schedulable account in the
// matched pool was rejected purely because it does not serve the requested model
// NAME, the failure is a CLIENT error (wrong/legacy model id), not capacity —
// return service.ErrUnsupportedModel so the handler maps it to HTTP 400
// invalid_request_error (kept out of upstream_error_rate). Otherwise the original
// empty-pool errors are preserved verbatim (429 fast-fail / platform label).
//
// Prod 2026-06-13: a client requesting unservable newapi names (deepseek-chat,
// qwen-max — the matched pools only mapped deepseek-v4-* / qwen3.7-*) produced
// empty-pool 429s (account_id=null) that read as a capacity signal and fired ops
// alerts. Routing both pool-emptied points through this one function converges the
// openai/newapi pools with the anthropic 400 behavior. The len(accounts)==0 branch
// (no schedulable account seen at all → no model evidence) and the post-load-balance
// "couldn't acquire a slot" branches stay 429: those are genuine capacity gaps.
//
// openAICompatNoCandidateEval carries scheduler context needed to classify a
// pool-emptied failure as "model unservable in this group" vs transient capacity.
// When nil, collectOpenAICompatSelectionFailureStats falls back to account-level
// model_mapping only (legacy unit tests).
type openAICompatNoCandidateEval struct {
	ctx                context.Context
	svc                *OpenAIGatewayService
	groupID            *int64
	requireCompact     bool
	requiredCapability OpenAIEndpointCapability
}

// tkOpenAICompatChannelPricingRestrictionError reports that the requested model is
// outside the group's channel servable/pricing allowlist. Caller fault → HTTP 400
// (ErrUnsupportedModel), not an empty-pool 429 that pollutes SLA / capacity alerts.
func tkOpenAICompatChannelPricingRestrictionError(requestedModel string) error {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return fmt.Errorf("%w (channel pricing restriction)", ErrUnsupportedModel)
	}
	return fmt.Errorf("%w: %s (channel pricing restriction)", ErrUnsupportedModel, requestedModel)
}

func openAICompatNoCandidateError(requestedModel, groupPlatform string, compactBlocked bool, accounts []Account, excludedIDs map[int64]struct{}, eval *openAICompatNoCandidateEval) error {
	if requestedModel != "" {
		var stats selectionFailureStats
		if eval != nil && eval.svc != nil && eval.ctx != nil {
			stats = eval.svc.collectOpenAICompatSelectionFailureStatsForRequest(
				eval.ctx,
				eval.groupID,
				requestedModel,
				eval.requireCompact,
				accounts,
				excludedIDs,
			)
		} else {
			stats = collectOpenAICompatSelectionFailureStats(accounts, requestedModel, excludedIDs)
		}
		if tkSelectionFailedDueToUnsupportedModel(stats) {
			return fmt.Errorf("%w: %s (%s)", ErrUnsupportedModel, requestedModel, summarizeSelectionFailureStats(stats))
		}
	}
	if groupPlatform != "" && groupPlatform != PlatformOpenAI {
		return fmt.Errorf("no available accounts for platform %q", openAICompatErrorPlatformLabel(groupPlatform))
	}
	return noAvailableOpenAISelectionError(requestedModel, compactBlocked, groupPlatform)
}

// collectOpenAICompatSelectionFailureStats categorizes each schedulable account
// in the group's pool using account-level model_mapping only. Prefer
// collectOpenAICompatSelectionFailureStatsForRequest when channel upstream
// restrictions apply.
func collectOpenAICompatSelectionFailureStats(accounts []Account, requestedModel string, excludedIDs map[int64]struct{}) selectionFailureStats {
	stats := selectionFailureStats{Total: len(accounts)}
	for i := range accounts {
		acc := &accounts[i]
		if excludedIDs != nil {
			if _, excluded := excludedIDs[acc.ID]; excluded {
				stats.Unschedulable++
				continue
			}
		}
		if requestedModel != "" && !acc.IsModelSupported(requestedModel) {
			stats.ModelUnsupported++
			stats.SampleMappingIDs = appendSelectionFailureSampleID(stats.SampleMappingIDs, acc.ID)
			continue
		}
		stats.Unschedulable++
	}
	return stats
}

// collectOpenAICompatSelectionFailureStatsForRequest mirrors the scheduler's
// model-servability axis: account model_mapping AND per-account upstream channel
// restrictions (BillingModelSourceUpstream). Prod 2026-07: a Qwen-group direct
// key hammering gpt-5.4-mini produced routing/platform 429s because passthrough
// accounts reported IsModelSupported=true while channel upstream pricing excluded
// the upstream model — those accounts must count as model_unsupported (client
// fault), not unschedulable capacity.
func (s *OpenAIGatewayService) collectOpenAICompatSelectionFailureStatsForRequest(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	requireCompact bool,
	accounts []Account,
	excludedIDs map[int64]struct{},
) selectionFailureStats {
	stats := selectionFailureStats{Total: len(accounts)}
	needsUpstreamCheck := s != nil && s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	for i := range accounts {
		acc := &accounts[i]
		if excludedIDs != nil {
			if _, excluded := excludedIDs[acc.ID]; excluded {
				stats.Unschedulable++
				continue
			}
		}
		if requestedModel != "" && s.isOpenAICompatModelUnservableForRequest(ctx, groupID, acc, requestedModel, requireCompact, needsUpstreamCheck) {
			stats.ModelUnsupported++
			stats.SampleMappingIDs = appendSelectionFailureSampleID(stats.SampleMappingIDs, acc.ID)
			continue
		}
		stats.Unschedulable++
	}
	return stats
}

func (s *OpenAIGatewayService) isOpenAICompatModelUnservableForRequest(
	ctx context.Context,
	groupID *int64,
	account *Account,
	requestedModel string,
	requireCompact bool,
	needsUpstreamCheck bool,
) bool {
	if account == nil || strings.TrimSpace(requestedModel) == "" {
		return false
	}
	if !account.IsModelSupported(requestedModel) {
		return true
	}
	if needsUpstreamCheck && groupID != nil && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel, requireCompact) {
		return true
	}
	return false
}
