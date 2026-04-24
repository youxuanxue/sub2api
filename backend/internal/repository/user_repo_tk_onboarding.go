package repository

import (
	"context"
	"time"

	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// MarkOnboardingTourSeen writes users.onboarding_tour_seen_at = NOW() exactly
// once per user. Idempotency lives in the WHERE clause: a repeat call is a
// no-op because the seen_at IS NULL predicate filters the row out, preserving
// the canonical first-seen timestamp (US-031 AC-007). Caller (handler) sits
// behind JWTAuth so we do not post-check existence — `affected == 0` always
// means "already seen", never "no such user".
func (r *userRepository) MarkOnboardingTourSeen(ctx context.Context, userID int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.User.Update().
		Where(
			dbuser.IDEQ(userID),
			dbuser.OnboardingTourSeenAtIsNil(),
		).
		SetOnboardingTourSeenAt(time.Now()).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	return nil
}
