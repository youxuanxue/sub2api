package service

import (
	"context"
	"log/slog"
	"strings"
)

type openAICompatSelectionFailureDiagnosis struct {
	Category string
	Detail   string
}

func (s *OpenAIGatewayService) collectOpenAICompatSelectionFailureStatsForRequest(
	ctx context.Context,
	groupID *int64,
	platform string,
	requestedModel string,
	requireCompact bool,
	requiredCapability OpenAIEndpointCapability,
	accounts []Account,
	excludedIDs map[int64]struct{},
) selectionFailureStats {
	stats := selectionFailureStats{Total: len(accounts)}
	needsUpstreamCheck := s != nil && s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	for i := range accounts {
		acc := &accounts[i]
		diagnosis := s.diagnoseOpenAICompatSelectionFailure(
			ctx,
			groupID,
			platform,
			acc,
			requestedModel,
			requireCompact,
			requiredCapability,
			excludedIDs,
			needsUpstreamCheck,
		)
		switch diagnosis.Category {
		case "excluded":
			stats.Excluded++
		case "runtime_blocked":
			stats.RuntimeBlocked++
			stats.SampleRuntimeBlockedIDs = appendSelectionFailureSampleID(stats.SampleRuntimeBlockedIDs, acc.ID)
		case "model_unsupported":
			stats.ModelUnsupported++
			stats.SampleMappingIDs = appendSelectionFailureSampleID(stats.SampleMappingIDs, acc.ID)
		case "model_rate_limited":
			stats.ModelRateLimited++
			remaining := acc.GetRateLimitRemainingTimeWithContext(ctx, requestedModel).Truncate(0)
			stats.SampleRateLimitIDs = appendSelectionFailureRateSample(stats.SampleRateLimitIDs, acc.ID, remaining)
		case "unschedulable":
			stats.Unschedulable++
			// Carry the naming reason, not just the count: this bucket absorbs every
			// scheduling gate, so a bare count cannot be acted on (2026-08-25).
			stats.SampleUnschedulableReasons = appendSelectionFailureReasonSample(
				stats.SampleUnschedulableReasons, acc.ID, diagnosis.Detail)
		case openAIProfitFilterReasonThreshold:
			stats.ProfitThreshold++
		case openAIProfitFilterReasonInvalidAccountRate:
			stats.ProfitInvalidRate++
		default:
			stats.Eligible++
		}
	}
	return stats
}

func (s *OpenAIGatewayService) diagnoseOpenAICompatSelectionFailure(
	ctx context.Context,
	groupID *int64,
	platform string,
	acc *Account,
	requestedModel string,
	requireCompact bool,
	requiredCapability OpenAIEndpointCapability,
	excludedIDs map[int64]struct{},
	needsUpstreamCheck bool,
) openAICompatSelectionFailureDiagnosis {
	if acc == nil {
		return openAICompatSelectionFailureDiagnosis{Category: "unschedulable", Detail: "account_nil"}
	}
	if excludedIDs != nil {
		if _, excluded := excludedIDs[acc.ID]; excluded {
			return openAICompatSelectionFailureDiagnosis{Category: "excluded"}
		}
	}
	platform = normalizeOpenAICompatiblePlatform(platform)
	if requestedModel != "" && s.isOpenAICompatModelUnservableForRequest(ctx, groupID, acc, requestedModel, requireCompact, needsUpstreamCheck) {
		return openAICompatSelectionFailureDiagnosis{
			Category: "model_unsupported",
			Detail:   "model_or_channel",
		}
	}
	// Ask WHICH gate rejected the account, not merely whether one did: the reason
	// string is what turns a bare "unschedulable=1" into an actionable line. See
	// openai_gateway_scheduling_tk_eligibility_reason.go for the 2026-08-25 incident.
	if reason := openAICompatEligibilityReason(ctx, acc, platform, requestedModel, requireCompact, requiredCapability); reason != "" {
		if requestedModel != "" && !acc.IsSchedulableForModelWithContext(ctx, requestedModel) {
			remaining := acc.GetRateLimitRemainingTimeWithContext(ctx, requestedModel)
			if remaining > 0 {
				return openAICompatSelectionFailureDiagnosis{
					Category: "model_rate_limited",
					Detail:   remaining.String(),
				}
			}
		}
		return openAICompatSelectionFailureDiagnosis{Category: "unschedulable", Detail: reason}
	}
	if vetoed, reason := openAIProfitControlVetoReason(ctx, acc); vetoed {
		return openAICompatSelectionFailureDiagnosis{Category: reason}
	}
	if s.isOpenAIAccountRuntimeBlocked(acc) {
		return openAICompatSelectionFailureDiagnosis{Category: "runtime_blocked", Detail: "whole_account"}
	}
	return openAICompatSelectionFailureDiagnosis{Category: "eligible"}
}

func (s *OpenAIGatewayService) logOpenAICompatSelectionFailure(
	ctx context.Context,
	eval *openAICompatNoCandidateEval,
	requestedModel string,
	stats selectionFailureStats,
) {
	if s == nil || eval == nil {
		return
	}
	groupID := derefGroupID(eval.groupID)
	platform := strings.TrimSpace(eval.platform)
	if platform == "" {
		platform = PlatformOpenAI
	}
	if !s.tkSelectionFailureLogDedup.shouldLog(groupID, platform, requestedModel, stats) {
		return
	}
	slog.Warn("openai_account_selection_failed",
		"group_id", groupID,
		"model", requestedModel,
		"platform", platform,
		"total", stats.Total,
		"eligible", stats.Eligible,
		"excluded", stats.Excluded,
		"unschedulable", stats.Unschedulable,
		"runtime_blocked", stats.RuntimeBlocked,
		"model_unsupported", stats.ModelUnsupported,
		"model_rate_limited", stats.ModelRateLimited,
		"sample_runtime_blocked", stats.SampleRuntimeBlockedIDs,
		"sample_model_unsupported", stats.SampleMappingIDs,
		"sample_model_rate_limited", stats.SampleRateLimitIDs,
		// The field that makes an unschedulable=N line actionable: which gate said no.
		"sample_unschedulable", stats.SampleUnschedulableReasons,
	)
	_ = ctx
}
