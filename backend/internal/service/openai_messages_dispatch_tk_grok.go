package service

const (
	defaultGrokMessagesDispatchOpusMappedModel   = "grok-4.5"
	defaultGrokMessagesDispatchSonnetMappedModel = "grok-4.3"
	defaultGrokMessagesDispatchHaikuMappedModel  = "grok-code-fast-1"
)

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
