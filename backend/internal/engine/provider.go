package engine

import "github.com/Wei-Shaw/sub2api/internal/domain"

const (
	ProviderNative       = "native"
	ProviderNewAPIBridge = "newapi_bridge"
)

const (
	BridgeEndpointChatCompletions = "chat_completions"
	BridgeEndpointResponses       = "responses"
	BridgeEndpointEmbeddings      = "embeddings"
	BridgeEndpointImages          = "images"
	BridgeEndpointVideoSubmit     = "video_submit"
	BridgeEndpointVideoFetch      = "video_fetch"
)

func OpenAICompatPlatforms() []string {
	return []string{domain.PlatformOpenAI, domain.PlatformNewAPI, domain.PlatformGrok}
}

func IsOpenAICompatPlatform(platform string) bool {
	if platform == "" {
		return false
	}
	for _, p := range OpenAICompatPlatforms() {
		if platform == p {
			return true
		}
	}
	return false
}

func IsOpenAICompatPoolMember(accountPlatform string, channelType int, groupPlatform string) bool {
	if accountPlatform == "" || groupPlatform == "" {
		return false
	}
	if accountPlatform != groupPlatform {
		return false
	}
	if groupPlatform == domain.PlatformNewAPI {
		return channelType > 0
	}
	return true
}

func AllSchedulingPlatforms() []string {
	return []string{
		domain.PlatformAnthropic,
		domain.PlatformGemini,
		domain.PlatformOpenAI,
		domain.PlatformAntigravity,
		domain.PlatformNewAPI,
		domain.PlatformKiro,
		domain.PlatformGrok,
	}
}

// apiKeyOnlySchedulingPlatforms lists scheduling platforms that authenticate
// per-request from a static credential (api key / channel secret) and therefore
// have NO background OAuth token to renew. They must be subtracted from
// AllSchedulingPlatforms() to obtain the OAuth-refresh set.
//
// newapi is the lone member: its accounts carry a channel api key (channel_type
// > 0), not an OAuth refresh_token, so the background refresh ticker skips them.
func apiKeyOnlySchedulingPlatforms() []string {
	return []string{domain.PlatformNewAPI}
}

// OAuthRefreshPlatforms is the SINGLE Go source of truth for which platforms the
// background token-refresh ticker renews. It is a projection of
// AllSchedulingPlatforms() minus apiKeyOnlySchedulingPlatforms(), NOT a
// hand-maintained parallel list — adding a scheduling platform forces an
// explicit "does it OAuth-refresh?" decision in apiKeyOnlySchedulingPlatforms().
//
// Two load-bearing consumers are verified against this list so a platform can
// never silently drop out of refresh (the R-001 failure class):
//   - repository.ListOAuthRefreshCandidates binds it as the SQL `platform =
//     ANY($1)` filter — there is no platform literal left in the SQL for an
//     upstream merge to reset to its four-platform default.
//   - the registered TokenRefresher set is asserted to cover exactly this list
//     (token_refresh_service_candidates_test.go), so dropping either a
//     refresher registration or a platform here fails the build's tests.
func OAuthRefreshPlatforms() []string {
	apiKeyOnly := apiKeyOnlySchedulingPlatforms()
	out := make([]string, 0, len(AllSchedulingPlatforms()))
	for _, p := range AllSchedulingPlatforms() {
		skip := false
		for _, a := range apiKeyOnly {
			if p == a {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, p)
		}
	}
	return out
}

// IsOAuthRefreshPlatform reports whether the platform is renewed by the
// background OAuth refresh ticker. Derived from OAuthRefreshPlatforms() so the
// predicate and the SQL filter can never disagree.
func IsOAuthRefreshPlatform(platform string) bool {
	if platform == "" {
		return false
	}
	for _, p := range OAuthRefreshPlatforms() {
		if platform == p {
			return true
		}
	}
	return false
}

// TrajProjectablePlatforms returns the platforms whose captured client-facing
// wire shape the traj v2 projector faithfully reconstructs (see
// trajectory.WireShapeForRecord). It is the SINGLE SOURCE the /auth/me
// projectable allowlist (frontend export-chip visibility) and the per-key
// export gate read from — never hand-list projectable platforms anywhere else.
//
// Derived from the wire-shape families, NOT a re-listed slice:
//   - anthropic + kiro relay the Anthropic /v1/messages shape;
//   - gemini (incl. vertex) and antigravity speak the Gemini contents[] shape
//     (antigravity additionally relays /v1/messages on /antigravity/v1, which
//     the projector dispatches per inbound endpoint);
//   - OpenAICompatPlatforms() speak the OpenAI chat/responses shapes.
//
// The OpenAI-compat members are pulled from OpenAICompatPlatforms() so adding a
// future compat platform there flows through here automatically.
func TrajProjectablePlatforms() []string {
	out := []string{
		domain.PlatformAnthropic,
		domain.PlatformKiro,
		domain.PlatformGemini,
		domain.PlatformAntigravity,
	}
	return append(out, OpenAICompatPlatforms()...)
}
