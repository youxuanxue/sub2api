package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	openAICompatIneligibleAccountNil        = "account_nil"
	openAICompatIneligibleNotPoolMember     = "not_pool_member"
	openAICompatIneligibleNotCompatible     = "not_openai_compatible"
	openAICompatIneligibleAccountCooling    = "account_cooling_or_disabled"
	openAICompatIneligibleQuotaAutoPause    = "quota_auto_pause"
	openAICompatIneligibleAuthorization     = "authorization_unavailable"
	openAICompatIneligibleModelUnsupported  = "model_unsupported_by_account"
	openAICompatIneligibleCapabilityMissing = "endpoint_capability_missing"
	openAICompatIneligibleCompactMissing    = "compact_unsupported"
	openAICompatIneligibleNoLegalRoute      = "no_legal_protocol_route"
)

// openAICompatIneligibleInputModality lives in candidate_multimodal_supply_tk.go
// next to ErrUnsupportedInputModality so the reason string cannot drift from the
// empty-pool 400 sentinel.

// openAICompatEligibilityReason returns "" when the account passes every
// ordinary scheduling gate, else a stable reason code naming the branch that
// rejected it. Gate ORDER and semantics must stay identical to the bool
// predicate below, which delegates here — they are the same code path, so they
// cannot drift.
func openAICompatEligibilityReason(
	ctx context.Context,
	account *Account,
	platform string,
	requestedModel string,
	requireCompact bool,
	requiredCapability OpenAIEndpointCapability,
) string {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if account == nil {
		return openAICompatIneligibleAccountNil
	}
	if source, ok := CompositeRouteSourceFromContext(ctx); ok && source == CompositeRouteSourceAccount {
		if model, present := RequestedPublicModelFromContext(ctx); present && !explicitModelMappingClaims(*account, model) {
			return "account_model_not_owned"
		}
	}
	if !account.IsOpenAICompatPoolMember(platform) {
		return fmt.Sprintf("%s(account_platform=%s requested=%s)",
			openAICompatIneligibleNotPoolMember, account.Platform, platform)
	}
	if !account.IsOpenAICompatible() {
		return openAICompatIneligibleNotCompatible
	}
	if !account.IsSchedulableForModelWithContext(ctx, requestedModel) {
		// Distinguishing "whole account not schedulable" from "this model is
		// cooling" is what the caller needs; it re-checks the model cooldown to
		// classify model_rate_limited, so only name the account-level case here.
		return openAICompatIneligibleAccountCooling
	}
	if account.IsOpenAI() {
		if paused, reason := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
			return fmt.Sprintf("%s(window=%s threshold=%.2f utilization=%.2f)",
				openAICompatIneligibleQuotaAutoPause, reason.window, reason.threshold, reason.utilization)
		}
	}
	if account.IsGrok() {
		if paused, reason := shouldAutoPauseGrokAccountByQuota(account); paused {
			return fmt.Sprintf("%s(window=%s threshold=%.2f utilization=%.2f)",
				openAICompatIneligibleQuotaAutoPause, reason.window, reason.threshold, reason.utilization)
		}
	}
	return openAIRequestEligibilityReason(ctx, account, requestedModel, requireCompact, requiredCapability)
}

// openAIRequestEligibilityReason owns request support for both scheduler modes.
// Account state and pool admission are evaluated separately from compatibility.
func openAIRequestEligibilityReason(ctx context.Context, account *Account, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) string {
	admissionModel := requestedModel
	if requireCompact && account != nil {
		if compactModel, matched := account.ResolveCompactMappedModel(strings.TrimSpace(requestedModel)); matched {
			compactModel = strings.TrimSpace(compactModel)
			if compactModel != "" {
				if isRetiredOpenAISubscriptionModel(account, compactModel) {
					return openAICompatIneligibleModelUnsupported
				}
				// Admit the compact target. The public name may be a retired
				// alias whose administrator replacement is still schedulable.
				admissionModel = compactModel
			}
		}
	}
	if admissionModel != "" && isRetiredOpenAISubscriptionModel(account, resolveOpenAIAccountUpstreamModelForRequest(account, admissionModel, false)) {
		return openAICompatIneligibleModelUnsupported
	}
	if !protocolRuntimeAuthorizationReady(ctx, account) {
		return openAICompatIneligibleAuthorization
	}
	// Multimodal supply gate before Plan (same order as candidateSupportsRequest).
	// Chat-only supplies reject non-convertible audio at Plan; modality must win
	// so Volc kimi-k3 audio keeps the known-negative 400, not no_legal_route.
	// docs/approved/multimodal-supply-capability-ssot.md
	if modality := accountInputModalityRejection(ctx, account, requestedModel); modality != "" {
		return fmt.Sprintf("%s(%s)", openAICompatIneligibleInputModality, modality)
	}
	if eligible, reason := protocolRequestEligibilityReason(ctx, account, admissionModel); !eligible {
		if reason == "model_not_supported" {
			return openAICompatIneligibleModelUnsupported
		}
		if detail := tkProtocolRouteIllegalReason(ctx, account, requestedModel); detail != "" {
			return detail
		}
		return openAICompatIneligibleNoLegalRoute
	}
	if !protocolRoutingOwnsOpenAITextCapability(ctx, requiredCapability) &&
		!account.SupportsOpenAIEndpointCapability(requiredCapability) {
		detail := string(requiredCapability)
		if account.IsGrok() && requiredCapability == OpenAIEndpointCapabilityGrokMediaGeneration {
			if _, grokReason := account.GrokMediaGenerationEligibility(); grokReason != "" {
				detail = grokReason
			}
		}
		return fmt.Sprintf("%s(%s)", openAICompatIneligibleCapabilityMissing, detail)
	}
	if requireCompact && openAICompactSupportTier(account) == 0 {
		return openAICompatIneligibleCompactMissing
	}
	return ""
}

// tkProtocolRouteIllegalReason names why protocol routing refused this
// (account, model) pair, or "" when the route is legal / the account is not
// governed. The upstream error text is carried verbatim: for the 2026-08-25
// incident it is the difference between "unschedulable" and "the account only
// declares supported_protocols=[responses], and this inbound shape has no
// preserving route to it".
func tkProtocolRouteIllegalReason(ctx context.Context, account *Account, requestedModel string) string {
	_, governed, err := protocolPlanForAccount(ctx, account, requestedModel)
	if !governed || err == nil {
		return ""
	}
	detail := strings.TrimSpace(err.Error())
	if errors.Is(err, ErrProtocolRouteUnavailable) {
		// Strip the wrapper prefix so the reason reads as one clause; the code
		// itself already says the route was unavailable.
		detail = strings.TrimSpace(strings.TrimPrefix(detail, ErrProtocolRouteUnavailable.Error()+":"))
	}
	if detail == "" {
		return openAICompatIneligibleNoLegalRoute
	}
	return fmt.Sprintf("%s(%s)", openAICompatIneligibleNoLegalRoute, detail)
}
