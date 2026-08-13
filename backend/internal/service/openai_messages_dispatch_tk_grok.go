package service

const (
	defaultGrokMessagesDispatchOpusMappedModel   = "grok-4.6"
	defaultGrokMessagesDispatchSonnetMappedModel = "grok-4.5"
	defaultGrokMessagesDispatchHaikuMappedModel  = "grok-code-fast-1"
)

// Grok /v1/messages dispatch intentionally moved up with the Grok 4.6
// refresh: opus -> 4.6, sonnet -> 4.5, while grok-4.3 remains a lower tier
// explicit model and pricing/catalog alias rather than the default sonnet tier.

func defaultMessagesDispatchMappedModelForPlatform(platform string, family string) string {
	switch platform {
	case PlatformGrok:
		switch family {
		case "opus":
			return defaultGrokMessagesDispatchOpusMappedModel
		case "sonnet":
			return defaultGrokMessagesDispatchSonnetMappedModel
		case "haiku":
			return defaultGrokMessagesDispatchHaikuMappedModel
		}
	case PlatformOpenAI:
		switch family {
		case "opus":
			return defaultOpenAIMessagesDispatchOpusMappedModel
		case "sonnet":
			return defaultOpenAIMessagesDispatchSonnetMappedModel
		case "haiku":
			return defaultOpenAIMessagesDispatchHaikuMappedModel
		}
	}
	return ""
}
