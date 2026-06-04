package service

// TK-only: renewable admin session for the prod→edge "manage accounts" handoff
// (see handler/edge_tk_admin_session_handler.go and the prod overview's "管理账号"
// jump). Kept in a companion file so auth_service.go stays close to upstream shape
// (CLAUDE.md §5).
//
// Why a token PAIR instead of a single short-lived token
// ------------------------------------------------------
// The original handoff minted a single 5-minute access token with NO refresh
// token. That established a session on the edge, but the edge SPA's setToken only
// schedules a proactive refresh when a refresh_token is present in localStorage —
// so the handoff session hard-expired after 5 minutes and the operator was bounced
// to the edge login page mid-task (creating an account + running an interactive
// OAuth flow on the edge easily exceeds 5 minutes, and the operator does not hold
// the edge's password). The fix issues the SAME token pair a normal login does
// (access + refresh, via GenerateTokenPair), so the edge session self-renews like
// any other login and survives the full management session.
//
// Security posture is unchanged where it matters: the irreducible elevation gate
// is still user.IsAdmin() enforced in the handler BEFORE this is called — only an
// admin-owned mirror-stub key can obtain a session. The refresh token rides in the
// edge handoff URL FRAGMENT (never sent to the server) and is scrubbed from the
// address bar on load by EdgeHandoffView's history.replaceState — the same
// exposure profile as the existing OAuth login callbacks, which also deliver
// refresh_token via the URL.

import "context"

// GenerateEdgeAdminSessionTokenPair mints a renewable admin session (access +
// refresh) for the edge handoff. It is a thin TK-named wrapper over the normal
// login path GenerateTokenPair (fresh token family), so the edge's standard
// jwt_auth middleware validates the access token and its /auth/refresh endpoint
// rotates the pair — no edge-specific session machinery. The caller MUST have
// already verified user.IsAdmin(); this only signs.
func (s *AuthService) GenerateEdgeAdminSessionTokenPair(ctx context.Context, user *User) (*TokenPair, error) {
	return s.GenerateTokenPair(ctx, user, "")
}
