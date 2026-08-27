package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// TK: name the reason an OpenAI-compat account failed the scheduling gate.
//
// WHY (2026-08-25 incident): the edge us6 openai pool served
// "no available accounts ... total=1 eligible=0 unschedulable=1" for five hours.
// Every diagnostic field said the same thing — `unschedulable=1` — because the
// eligibility predicate has SEVEN independent `return false` branches that all
// collapsed into one bucket, and the one field that could have distinguished
// them (`openAICompatSelectionFailureDiagnosis.Detail`, hard-coded to the
// useless string "eligibility") was never emitted to any log at any level.
//
// Ruling that out cost a full day of probing: five separate hypotheses (5h
// quota window, lapsed subscription, DB schedulable flags, redis cooldown,
// model support) each had to be disproved by hand against prod and the upstream
// API, because the log could not say which predicate had actually fired. The
// real cause — ProtocolRouteLegal finding no legal route after protocol-routing
// governance took over the account — is the LAST of the seven branches, and the
// only one that was completely invisible.
//
// So: one reason string per branch, the route error text included verbatim, and
// the bool predicate becomes a thin wrapper so there is exactly one copy of the
// gate order. A future reader gets the answer from the log line instead of a
// day of bisection.
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
	if !protocolRuntimeAuthorizationReady(ctx, account) {
		return openAICompatIneligibleAuthorization
	}
	if requestedModel != "" && !account.IsModelSupported(requestedModel) {
		return openAICompatIneligibleModelUnsupported
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
	if reason := tkProtocolRouteIllegalReason(ctx, account, requestedModel); reason != "" {
		return reason
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
