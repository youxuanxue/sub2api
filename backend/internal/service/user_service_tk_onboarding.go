package service

import (
	"context"
	"fmt"
)

// MarkOnboardingTourSeen records that the user has completed the onboarding tour.
//
// PR 2 (US-031) P1-A: server-side persistence so the "seen" memory survives
// localStorage clears / device switches. Idempotent — calling twice does not
// move the timestamp (see repository implementation).
func (s *UserService) MarkOnboardingTourSeen(ctx context.Context, userID int64) error {
	if err := s.userRepo.MarkOnboardingTourSeen(ctx, userID); err != nil {
		return fmt.Errorf("mark onboarding tour seen: %w", err)
	}
	return nil
}
