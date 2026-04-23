package repository

import (
	"context"
	"time"

	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// MarkOnboardingTourSeen writes users.onboarding_tour_seen_at = NOW() once
// (idempotent — second call leaves the existing timestamp untouched).
//
// Called from POST /api/v1/user/onboarding-tour-completed (US-031 PR 2 P1-A).
// Server-side persistence avoids the localStorage-only fallback that broke
// "已经看过" memory across browsers / devices / cache clears.
//
// Idempotency rationale (US-031 AC-007): if a user manually replays the tour
// then completes it again, we do NOT want to bump the timestamp — it would
// otherwise look like the user "saw the tour again on every dashboard refresh".
// We treat the first completion as the canonical seen-at moment.
func (r *userRepository) MarkOnboardingTourSeen(ctx context.Context, userID int64) error {
	client := clientFromContext(ctx, r.client)
	now := time.Now()
	affected, err := client.User.Update().
		Where(
			dbuser.IDEQ(userID),
			dbuser.OnboardingTourSeenAtIsNil(),
		).
		SetOnboardingTourSeenAt(now).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	// affected == 0 means either the user does not exist OR they already have
	// a non-NULL onboarding_tour_seen_at. We treat both as success (idempotent),
	// but verify the user actually exists so a 404 still surfaces correctly.
	if affected == 0 {
		exists, err := client.User.Query().Where(dbuser.IDEQ(userID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return service.ErrUserNotFound
		}
	}
	return nil
}
