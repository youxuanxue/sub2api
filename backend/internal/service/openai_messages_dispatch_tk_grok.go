package service

const (
	defaultGrokMessagesDispatchOpusMappedModel   = "grok-4.6"
	defaultGrokMessagesDispatchSonnetMappedModel = "grok-4.5"
	defaultGrokMessagesDispatchHaikuMappedModel  = "grok-code-fast-1"
)

// Grok /v1/messages dispatch defaults are owned by
// tk_messages_dispatch_family_registry.json; the constants below are runtime
// mirrors mechanically checked by scripts/checks/messages-dispatch-family-drift.py.

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
