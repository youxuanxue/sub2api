package repository

import (
	"context"
	"time"

	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// MarkOnboardingTourSeen writes users.onboarding_tour_seen_at = NOW() once.
//
// Idempotency lives in the WHERE clause: only rows whose seen_at IS NULL get
// updated. A repeat call (e.g. user replays tour, completes again) is a no-op
// because the predicate filters their row out — the canonical "first seen"
// timestamp is preserved (US-031 AC-007).
//
// Caller contract: this is invoked from the POST /user/onboarding-tour-completed
// handler, which sits behind JWTAuth middleware (jwt_auth.go) that has already
// validated the user exists in DB this request. We therefore do NOT
// post-check existence here — `affected == 0` always means "already seen",
// never "no such user".
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
