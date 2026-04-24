/**
 * Onboarding API client (TokenKey-only).
 *
 * Lives in its own file (per CLAUDE.md §5) so the upstream-shaped
 * `frontend/src/api/user.ts` stays at zero TK diff. Backed by
 * `POST /api/v1/user/onboarding-tour-completed`
 * (`handler/user_handler_tk_onboarding.go`, US-031 / docs/approved/user-cold-start.md §5 P1-A).
 *
 * Idempotent on the server (a second call does not move the recorded
 * timestamp, US-031 AC-007). Best-effort: if this throws, the next
 * dashboard mount re-launches the tour because the server still returns
 * `onboarding_tour_seen_at: null`.
 */

import { apiClient } from './client'

export async function markOnboardingTourSeen(): Promise<void> {
  await apiClient.post('/user/onboarding-tour-completed')
}
