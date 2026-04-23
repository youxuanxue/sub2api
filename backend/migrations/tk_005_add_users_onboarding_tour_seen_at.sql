-- US-031 PR 2 P1-A: persist "user has seen the onboarding tour" server-side
-- so that clearing localStorage / switching browsers does not re-trigger the tour.
-- NULL (default) = never seen, auto-start on next dashboard mount.
-- Non-NULL = seen at this timestamp, do not auto-start (replayTour() still works).
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS onboarding_tour_seen_at timestamptz NULL;

COMMENT ON COLUMN users.onboarding_tour_seen_at IS
    'Timestamp when the user first completed the onboarding tour. NULL = never seen → auto-start. Persisted server-side per US-031 to survive cache clears / device switches.';
