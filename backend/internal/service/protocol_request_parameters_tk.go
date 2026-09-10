package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/tidwall/gjson"
)

// These exclusions preserve explicit client intent on empirically rejected
// provider/model paths. They do not remove parameters or alter model mappings.
func protocolRequestParametersSupported(account *Account, resolvedModel string, request *protocolrouter.CanonicalRequest) bool {
	if request == nil {
		return true
	}
	if account.IsCursor() {
		choice := request.Profile().ToolChoice
		return choice != protocolrouter.ToolChoiceRequired && choice != protocolrouter.ToolChoiceNamed
	}
	if isNewAPINVIDIABuildAccount(account) && resolvedModel == nvidiaBuildModelTargets["deepseek-v4-pro"] &&
		request.InboundProtocol() == protocolrouter.ProtocolChatCompletions {
		return !gjson.GetBytes(request.Body(), "enable_thinking").Exists()
	}
	if account.Platform != PlatformGrok {
		return true
	}
	model := strings.ToLower(xai.StripGrokProviderPrefix(strings.TrimSpace(resolvedModel)))
	if model != "grok-4.6" && model != "grok-4.6-latest" {
		return true
	}
	var fields []string
	switch request.InboundProtocol() {
	case protocolrouter.ProtocolChatCompletions:
		fields = []string{"reasoning_effort", "reasoningEffort"}
	case protocolrouter.ProtocolResponses:
		fields = []string{"reasoning.effort", "reasoning_effort", "reasoningEffort"}
	default:
		return true
	}
	body := request.Body()
	for _, field := range fields {
		value := gjson.GetBytes(body, field)
		if value.Type != gjson.String {
			continue
		}
		if normalized, keep := normalizeGrokReasoningEffortValue(value.String(), resolvedModel); keep && normalized == "none" {
			return false
		}
	}
	return true
}
