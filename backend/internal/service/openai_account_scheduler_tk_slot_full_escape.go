package service

import "context"

func (s *defaultOpenAIAccountScheduler) stickySlotFullEscapeEnabled(ctx context.Context) bool {
	if s == nil || s.service == nil {
		return true
	}
	if s.service.settingService == nil {
		if s.service.cfg != nil {
			cfg := s.service.cfg.Gateway.OpenAIScheduler
			if !cfg.StickyEscapeEnabled && (cfg.StickyEscapeTTFTMs > 0 || cfg.StickyEscapeErrorRate > 0) {
				return false
			}
		}
		return true
	}
	return s.service.settingService.IsStickySlotFullEscapeEnabled(ctx)
}

// tkSelectStickySessionPhase runs TokenKey sticky session selection with
// slot-full escape (#2859). When early is non-nil the caller must return it;
// otherwise stickyWaitPlan may be true so a later load-balance miss falls back
// to waiting on the sticky account.
func (s *defaultOpenAIAccountScheduler) tkSelectStickySessionPhase(
	ctx context.Context,
	req *OpenAIAccountScheduleRequest,
	markStickyHit func(*AccountSelectionResult),
) (stickySel *AccountSelectionResult, stickyWaitPlan bool, early *AccountSelectionResult, err error) {
	if req.StickyWeighted {
		return nil, false, nil, nil
	}
	var escapedSticky bool
	var escapedStickyAccountID int64
	stickySel, escapedSticky, escapedStickyAccountID, err = s.selectBySessionHash(ctx, *req)
	if err != nil {
		return nil, false, nil, err
	}
	if stickySel != nil && stickySel.Acquired {
		markStickyHit(stickySel)
		return stickySel, false, stickySel, nil
	}

	stickyWaitPlan = stickySel != nil && stickySel.Account != nil
	if stickyWaitPlan && !s.stickySlotFullEscapeEnabled(ctx) {
		markStickyHit(stickySel)
		return stickySel, stickyWaitPlan, stickySel, nil
	}
	if escapedSticky {
		req.PreserveStickyBinding = true
		if escapedStickyAccountID > 0 {
			req.ExcludedIDs = cloneExcludedAccountIDs(req.ExcludedIDs)
			if req.ExcludedIDs == nil {
				req.ExcludedIDs = make(map[int64]struct{})
			}
			req.ExcludedIDs[escapedStickyAccountID] = struct{}{}
		}
	}
	return stickySel, stickyWaitPlan, nil, nil
}

// tkOpenAICompatEndpointCapabilityAllows is the TokenKey fifth-platform gate for
// accountSupportsOpenAICapabilities: upstream IsOpenAI()-only checks would
// fail-closed every newapi account, so endpoint capability applies to native
// OpenAI (and Grok media) only.
func tkOpenAICompatEndpointCapabilityAllows(account *Account, requiredCapability OpenAIEndpointCapability) bool {
	if account == nil {
		return false
	}
	if account.IsOpenAI() && !account.SupportsOpenAIEndpointCapability(requiredCapability) {
		return false
	}
	if account.IsGrok() && requiredCapability == OpenAIEndpointCapabilityGrokMediaGeneration &&
		!account.SupportsOpenAIEndpointCapability(requiredCapability) {
		return false
	}
	return true
}
