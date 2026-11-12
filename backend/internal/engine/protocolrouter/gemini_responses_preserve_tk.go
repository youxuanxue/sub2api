package protocolrouter

import (
	"encoding/json"
	"strings"

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
	if json.Unmarshal(req.Body(), &responses) != nil || !responsesInputReachesChat(responses.Input) {
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

// responsesInputReachesChat rejects Responses item types that
// ResponsesToChatCompletionsRequest skips. Chat admission never sees those
// items, so a later true result would drop them.
func responsesInputReachesChat(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || strings.HasPrefix(trimmed, "\"") {
		return true
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false
	}
	for _, item := range items {
		piece := strings.TrimSpace(string(item))
		if piece == "" || piece == "null" || strings.HasPrefix(piece, "\"") {
			continue
		}
		var obj struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &obj) != nil || !apicompat.ResponsesInputItemReachesChat(obj.Type) {
			return false
		}
	}
	return true
}
