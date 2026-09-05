package service

import (
	"context"
	"strings"
)

// tkTrySelectByPreviousResponseID runs OpenAI previous_response_id sticky
// selection. handled is true when the caller must return (hit or error).
func (s *defaultOpenAIAccountScheduler) tkTrySelectByPreviousResponseID(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	decision OpenAIAccountScheduleDecision,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	previousResponseID := strings.TrimSpace(req.PreviousResponseID)
	// Production Select() only sets GroupPlatform. Empty req.Platform normalizes
	// to openai, so the gate must use schedulePlatform() — the same SSOT as
	// sticky/load-balance — or newapi/grok/CN groups inherit OpenAI response sticky.
	if previousResponseID == "" || req.schedulePlatform() != PlatformOpenAI ||
		(req.StickyWeighted && req.PreviousResponseCanMove) {
		return nil, decision, false, nil
	}
	if s == nil || s.service == nil {
		return nil, decision, false, nil
	}

	selection, err := s.service.selectAccountByPreviousResponseIDForCapability(
		ctx,
		req.GroupID,
		previousResponseID,
		req.RequestedModel,
		req.ExcludedIDs,
		req.RequiredCapability,
		req.RequireCompact,
	)
	if err != nil {
		return nil, decision, true, err
	}
	if selection != nil && selection.Account != nil {
		compatible, _ := s.isAccountRequestCompatibleReason(ctx, selection.Account, req)
		if !compatible ||
			!s.isAccountTransportCompatible(selection.Account, req.RequiredTransport) ||
			!s.service.openAIAccountMatchesSchedulingGroup(ctx, selection.Account, req.GroupID, req.schedulePlatform()) {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			selection = nil
		}
	}
	if selection != nil && selection.Account != nil {
		decision.Layer = openAIAccountScheduleLayerPreviousResponse
		decision.StickyPreviousHit = true
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		if req.SessionHash != "" {
			_ = s.service.bindOpenAIStickySessionDuringSelection(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
		}
		return selection, decision, true, nil
	}
	return nil, decision, false, nil
}
