package protocolrouter

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// preservesResponsesToGemini admits a Responses request when the Chat bridge
// keeps it and Chat→Gemini would. Root Responses is the only path; audio,
// continuation, and reasoning summaries stay on a native Responses account.
func preservesResponsesToGemini(req CanonicalRequest) bool {
	if req.inboundProtocol == ProtocolResponses && req.responsesPath != ResponsesPathRoot {
		return false
	}
	if req.profile.Continuation != ContinuationNone || req.profile.Reasoning == ReasoningSummary {
		return false
	}
	if req.profile.ContentKinds == 0 || req.profile.ContentKinds&^(ContentText|ContentImage|ContentUnknown) != 0 {
		return false
	}
	var responses apicompat.ResponsesRequest
	if json.Unmarshal(req.Body(), &responses) != nil {
		return false
	}
	chat, err := apicompat.ResponsesToChatCompletionsRequest(&responses)
	if err != nil || chat == nil {
		return false
	}
	raw, err := json.Marshal(chat)
	if err != nil {
		return false
	}
	bridged, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, req.RequestedModel(), req.Profile().Stream, raw)
	if err != nil {
		return false
	}
	return preservesChatToGemini(bridged)
}
