/**
 * Onboarding API client (TokenKey-only).
 *
 * Lives in its own file (per CLAUDE.md §5) so the upstream-shaped
 * `frontend/src/api/user.ts` does not need a TK-specific edit just to host
 * one helper — keeping that file at zero diff vs upstream minimizes merge
 * friction whenever Wei-Shaw/sub2api rebases the user API surface.
 *
 * Backed by POST /api/v1/user/onboarding-tour-completed
 * (handler/user_handler_tk_onboarding.go). docs/approved/user-cold-start.md §5 P1-A.
 */

import { apiClient } from './client'

/**
 * Mark the onboarding tour as completed for the current user.
 * Idempotent on the server: a second call after the first does not move
 * the recorded timestamp (US-031 AC-007).
 *
 * Best-effort: if this throws (network error, 5xx, etc.), the dashboard
 * will simply re-launch the tour on the next mount because
 * `user.onboarding_tour_seen_at` will still be `null` from the server.
 */
export async function markOnboardingTourSeen(): Promise<void> {
  await apiClient.post('/user/onboarding-tour-completed')
}

export const onboardingAPI = {
  markOnboardingTourSeen
}

export default onboardingAPI
