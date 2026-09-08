package service

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// GroupCandidateEligibility separates support from current readiness.
// An empty live pool never proves that the user lacks model entitlement.
type GroupCandidateEligibility struct {
	Supported bool
	Available bool
	Saturated bool
}

type groupCandidateEvaluator func(context.Context, Group, string, UniversalShape) (GroupCandidateEligibility, error)

// candidateSupportsRequest owns account pool admission for Universal and model
// discovery. Governed text legality belongs exclusively to protocolrouter.Plan;
// native and media paths retain their existing capability owners.
func (s *GatewayService) candidateSupportsRequest(ctx context.Context, account *Account, platform string, useMixed bool, model string, shape UniversalShape) (bool, error) {
	if account == nil {
		return false, nil
	}
	if !s.isClaudeNewAPICrossPlatformAccountAllowed(ctx, account, platform, model, false) {
		if IsOpenAICompatPlatform(platform) {
			if !account.IsOpenAICompatPoolMember(platform) {
				return false, nil
			}
		} else if !s.isAccountAllowedForPlatformModel(ctx, account, platform, useMixed, model) {
			return false, nil
		}
	}
	if _, governed, err := protocolPlanForAccount(ctx, account, model); governed {
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, protocolrouter.ErrModelPolicyDenied):
			return false, ErrUniversalUnsupportedModel
		case errors.Is(err, ErrProtocolCapabilityUnknown):
			// Missing or conflicted capability evidence is an unknown candidate
			// state. It must not be reported as either model rejection or lack of
			// entitlement.
			return false, err
		case errors.Is(err, protocolrouter.ErrNoLegalRoute):
			return false, nil
		default:
			return false, err
		}
	}
	var supported bool
	if IsOpenAICompatPlatform(platform) {
		if !universalOpenAICompatAccountSupportsShape(account, shape) {
			return false, nil
		}
		supported = universalOpenAICompatAccountSupportsModel(ctx, s, account, model, shape)
	} else {
		supported = s.isModelSupportedByAccountWithContext(ctx, account, model)
	}
	// Native/media owners retain admission. Only an explicit model-policy
	// refusal can classify their rejection as a client unsupported-model error.
	if !supported && model != "" && !accountAdmitsRequestedModelWithContext(ctx, account, model) {
		return false, ErrUniversalUnsupportedModel
	}
	return supported, nil
}

// evaluateGroupCandidates is a read-only projection of existing scheduler
// filters. It does not reserve slots, mutate sticky bindings or choose billing.
func (s *GatewayService) evaluateGroupCandidates(ctx context.Context, openai *OpenAIGatewayService, group Group, model string, shape UniversalShape) (GroupCandidateEligibility, error) {
	var result GroupCandidateEligibility
	if s == nil || s.accountRepo == nil {
		return result, ErrUniversalCapabilityUnavailable
	}
	if !IsGroupContextValid(&group) && s.schedulerSnapshot == nil && s.groupRepo == nil {
		return result, ErrUniversalCapabilityUnavailable
	}
	ctx = s.withGroupContext(ctx, &group)
	gid := group.ID
	var accounts []Account
	var mixed bool
	var err error
	openAIPath := IsOpenAICompatPlatform(group.Platform) && shape != ShapeGemini
	if openAIPath && openai != nil {
		accounts, err = openai.listSchedulableAccounts(ctx, &gid, group.Platform)
	} else {
		accounts, mixed, err = s.listSchedulableAccountsForModel(ctx, &gid, group.Platform, false, model)
	}
	if err != nil {
		return result, err
	}
	var supportErr error
	unsupportedModel := false
	supported := make([]Account, 0, len(accounts))
	for i := range accounts {
		ok, err := s.candidateSupportsRequest(ctx, &accounts[i], group.Platform, mixed, model, shape)
		if err != nil {
			if errors.Is(err, ErrUniversalUnsupportedModel) {
				unsupportedModel = true
				continue
			}
			if supportErr == nil {
				supportErr = err
			}
			continue
		}
		if ok {
			supported = append(supported, accounts[i])
			result.Supported = true
		}
	}
	var candidates []*Account
	if openAIPath && openai != nil {
		ctx = openai.withOpenAIQuotaAutoPauseContext(ctx)
		ctx = openai.withOpenAIGroupPrivacyRequirement(ctx, &gid)
		if shape == ShapeOpenAIChat || shape == ShapeAnthropicMessages {
			ctx = openai.withOpenAIProfitControlGate(ctx, &gid)
		}
		req := OpenAIAccountScheduleRequest{GroupID: &gid, GroupPlatform: group.Platform,
			RequestedModel: model, RestrictionModel: model, RequirePrivacySet: group.RequirePrivacySet}
		switch shape {
		case ShapeOpenAIEmbeddings:
			req.RequiredCapability = OpenAIEndpointCapabilityEmbeddings
		case ShapeOpenAIImages, ShapeOpenAIImagesEdit:
			req.RequiredImageCapability = OpenAIImagesCapabilityBasic
		case ShapeOpenAIVideo:
			req.RequiredVideoSupport = true
		case ShapeOpenAIChat:
			req.RequiredCapability = OpenAIEndpointCapabilityChatCompletions
			if request, ok := ProtocolRoutingRequest(ctx); ok && request.InboundProtocol() == protocolrouter.ProtocolResponses {
				req.RequiredCapability = OpenAIEndpointCapabilityResponses
			}
		}
		scheduler := defaultOpenAIAccountScheduler{service: openai}
		candidates, _ = scheduler.openAICandidates(ctx, supported, req)
	} else {
		if shape == ShapeOpenAIChat || shape == ShapeAnthropicMessages || shape == ShapeGemini {
			ctx, _ = WithGatewayTokenRequestPricing(ctx)
		}
		ctx = s.withGatewayProfitControlGate(ctx, &gid)
		ctx = s.withRPMPrefetch(ctx, supported)
		ctx = s.withWindowCostPrefetch(ctx, supported)
		candidates = s.gatewayCandidates(ctx, supported, group.Platform, mixed, model, nil)
	}
	if len(candidates) > 0 {
		result.Available = true
		counts := s.candidateSaturationState().counts(ctx, candidates, model)
		result.Saturated = true
		for _, account := range candidates {
			if !candidateSaturated(counts[account.ID]) {
				result.Saturated = false
				break
			}
		}
		return result, nil
	}
	if result.Supported {
		return result, nil
	}
	// Scheduler snapshots intentionally omit disabled/error/cooling accounts.
	// Consult configured membership only to distinguish capacity from entitlement.
	all, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", gid, "", 0)
	if err != nil {
		return result, err
	}
	for i := range all {
		ok, err := s.candidateSupportsRequest(ctx, &all[i], group.Platform, mixed, model, shape)
		if err != nil {
			if errors.Is(err, ErrUniversalUnsupportedModel) {
				unsupportedModel = true
				continue
			}
			if supportErr == nil {
				supportErr = err
			}
			continue
		}
		if ok {
			result.Supported = true
			return result, nil
		}
	}
	if supportErr != nil {
		return result, supportErr
	}
	if unsupportedModel {
		return result, ErrUniversalUnsupportedModel
	}
	return result, nil
}
