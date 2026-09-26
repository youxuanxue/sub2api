package protocolrouter

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicpolicy"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// GeminiWebBestEffortTokenLimit identifies the explicitly accepted Web contract:
// output token budgets are not guaranteed by the provider.
const GeminiWebBestEffortTokenLimit = "gemini_web_best_effort_token_limit"

// compatibleProviderRequest preserves original request bytes/digest and creates
// only the provider-specific effective projection consumed by execution.
func compatibleProviderRequest(request CanonicalRequest, account AccountSnapshot) (CanonicalRequest, string, bool) {
	if account.providerCapability != ProviderCapabilityGeminiWeb {
		return request, "", true
	}
	var fields []string
	switch request.inboundProtocol {
	case ProtocolMessages:
		fields = []string{"max_tokens"}
		if !gjson.GetBytes(request.body, "max_tokens").Exists() {
			return request, "", false
		}
	case ProtocolChatCompletions:
		fields = []string{"max_tokens", "max_completion_tokens"}
	case ProtocolResponses:
		fields = []string{"max_output_tokens"}
	case ProtocolGeminiGenerateContent:
		fields = []string{"generationConfig.maxOutputTokens"}
	}
	body := request.body
	changed := false
	for _, field := range fields {
		value := gjson.GetBytes(body, field)
		if !value.Exists() {
			continue
		}
		var limit int64
		if value.Type != gjson.Number || json.Unmarshal([]byte(value.Raw), &limit) != nil || limit <= 0 {
			return request, "", false
		}
		var err error
		body, err = sjson.DeleteBytes(body, field)
		if err != nil {
			return request, "", false
		}
		changed = true
	}
	if !changed {
		return request, "", true
	}
	effective, err := ParseCanonicalRequest(request.inboundProtocol, request.responsesPath, request.requestedModel, request.profile.Stream, body)
	if err != nil {
		return request, "", false
	}
	return effective, GeminiWebBestEffortTokenLimit, true
}

func (a AccountSnapshot) requestCapabilities(target Protocol) anthropicpolicy.Capabilities {
	if capabilities, ok := a.modelCapabilities[target]; ok {
		return capabilities
	}
	if target == ProtocolMessages {
		return anthropicpolicy.MessagesCapabilities(a.resolvedModel)
	}
	return anthropicpolicy.Capabilities{ForcedToolsWithThinking: true}
}

func compatibleRequest(request CanonicalRequest, capabilities anthropicpolicy.Capabilities) (CanonicalRequest, string) {
	// plan() calls this once per candidate route, and every inbound protocol has
	// three or four of them, so a body validation here is repeated per route and
	// per account over bytes that cannot change within one request.
	messages := request.inboundProtocol == ProtocolMessages
	var body []byte
	var changed bool
	if request.bodyJSONValidated {
		// The facts were derived from these exact bytes and this same messages flag
		// by the constructor, so only the capability half is left to decide here.
		body, changed = anthropicpolicy.NormalizeWithFacts(request.body, messages, request.policyFacts, capabilities)
	} else {
		body, changed = anthropicpolicy.Normalize(request.body, messages, capabilities)
	}
	if !changed {
		return request, ""
	}
	effective, err := ParseCanonicalRequest(request.inboundProtocol, request.responsesPath, request.requestedModel, request.profile.Stream, body)
	if err != nil {
		return request, ""
	}
	return effective, anthropicpolicy.ThinkingPreferred
}
