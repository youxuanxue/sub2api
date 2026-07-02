package service

const (
	defaultGrokMessagesDispatchOpusMappedModel   = "grok-4.3"
	defaultGrokMessagesDispatchSonnetMappedModel = "grok-code-fast-1"
	defaultGrokMessagesDispatchHaikuMappedModel  = "grok-code-fast-1"
)

func defaultMessagesDispatchMappedModelForPlatform(platform string, family string) string {
	if platform == PlatformGrok {
		switch family {
		case "opus":
			return defaultGrokMessagesDispatchOpusMappedModel
		case "sonnet":
			return defaultGrokMessagesDispatchSonnetMappedModel
		case "haiku":
			return defaultGrokMessagesDispatchHaikuMappedModel
		}
		return ""
	}
	switch family {
	case "opus":
		return defaultOpenAIMessagesDispatchOpusMappedModel
	case "sonnet":
		return defaultOpenAIMessagesDispatchSonnetMappedModel
	case "haiku":
		return defaultOpenAIMessagesDispatchHaikuMappedModel
	default:
		return ""
	}
}
