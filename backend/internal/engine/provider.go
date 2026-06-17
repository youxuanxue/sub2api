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
