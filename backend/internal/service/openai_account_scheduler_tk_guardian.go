package service

import "context"

func (s *defaultOpenAIAccountScheduler) tkTrySelectGuardianParent(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	decision OpenAIAccountScheduleDecision,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	if req.GuardianParentAccountID <= 0 {
		return nil, decision, false, nil
	}
	parentReq := req
	parentReq.StickyAccountID = req.GuardianParentAccountID
	parentReq.PreserveStickyBinding = true
	selection, _, _, err := s.selectBySessionHash(ctx, parentReq)
	if err != nil {
		return nil, decision, true, err
	}
	if selection == nil || selection.Account == nil {
		return nil, decision, false, nil
	}
	decision.Layer = openAIAccountScheduleLayerGuardianParent
	decision.StickySessionHit = true
	decision.SelectedAccountID = selection.Account.ID
	decision.SelectedAccountType = selection.Account.Type
	return selection, decision, true, nil
}

func (s *OpenAIGatewayService) tkTrySelectAccountByGuardianParentWhenSchedulerNil(
	ctx context.Context,
	guardianParentAccountID int64,
	groupID *int64,
	groupPlatform string,
	sessionHash string,
	restrictionModel string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requiredVideoSupport bool,
	requireCompact bool,
	decision OpenAIAccountScheduleDecision,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	if guardianParentAccountID <= 0 {
		return nil, decision, false, nil
	}
	if s.checkChannelPricingRestriction(ctx, groupID, restrictionModel) {
		return nil, decision, true, s.tkGroupUnsupportedModelRecordErr(groupID, restrictionModel, tkOpenAICompatChannelPricingRestrictionError(restrictionModel))
	}
	fallbackScheduler := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
	selection, _, _, err := fallbackScheduler.selectBySessionHash(ctx, OpenAIAccountScheduleRequest{
		GroupID:                 groupID,
		GroupPlatform:           groupPlatform,
		SessionHash:             sessionHash,
		StickyAccountID:         guardianParentAccountID,
		PreserveStickyBinding:   true,
		RequestedModel:          requestedModel,
		RestrictionModel:        restrictionModel,
		RequiredTransport:       requiredTransport,
		RequiredCapability:      requiredCapability,
		RequiredImageCapability: requiredImageCapability,
		RequiredVideoSupport:    requiredVideoSupport,
		RequireCompact:          requireCompact,
		RequirePrivacySet:       s.openAIGroupRequiresPrivacySet(ctx, groupID),
		ExcludedIDs:             excludedIDs,
	})
	if err != nil {
		return nil, decision, true, err
	}
	if selection == nil || selection.Account == nil {
		return nil, decision, false, nil
	}
	decision.Layer = openAIAccountScheduleLayerGuardianParent
	decision.StickySessionHit = true
	decision.SelectedAccountID = selection.Account.ID
	decision.SelectedAccountType = selection.Account.Type
	return selection, decision, true, nil
}
