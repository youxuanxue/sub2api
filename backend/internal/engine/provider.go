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
	return []string{domain.PlatformOpenAI, domain.PlatformNewAPI}
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
	}
}

func BridgeEndpointEnabled(endpoint string) bool {
	switch endpoint {
	case BridgeEndpointChatCompletions, BridgeEndpointResponses, BridgeEndpointEmbeddings, BridgeEndpointImages,
		BridgeEndpointVideoSubmit, BridgeEndpointVideoFetch:
		return true
	default:
		return false
	}
}
