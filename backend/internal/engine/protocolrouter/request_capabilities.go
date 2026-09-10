package protocolrouter

import "github.com/Wei-Shaw/sub2api/internal/pkg/anthropicpolicy"

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
	body, changed := anthropicpolicy.Normalize(request.body, request.inboundProtocol == ProtocolMessages, capabilities)
	if !changed {
		return request, ""
	}
	effective, err := ParseCanonicalRequest(request.inboundProtocol, request.responsesPath, request.requestedModel, request.profile.Stream, body)
	if err != nil {
		return request, ""
	}
	return effective, anthropicpolicy.ThinkingPreferred
}
