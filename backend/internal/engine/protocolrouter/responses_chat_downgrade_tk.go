package protocolrouter

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ResponsesChatDroppedUnsupportedFields marks a Responses→Chat plan that
// dropped fields Chat Completions does not keep. Selection prefers rank 0
// (native Responses, fields kept) over this rank-1 plan.
const ResponsesChatDroppedUnsupportedFields = "responses_chat_drop_unsupported_fields"

// downgradeResponsesToChat removes prompt_cache_key, cache_control,
// previous_response_id, and audio parts so a Chat-only supply can still serve
// the request. The original request is returned unchanged when none of those
// fields are present. ok is false only when a body that claimed those fields
// cannot be rewritten.
func downgradeResponsesToChat(request CanonicalRequest) (CanonicalRequest, string, bool) {
	profile := request.Profile()
	if profile.Continuation != ContinuationPreviousResponse &&
		profile.PromptCache == PromptCacheNone &&
		profile.ContentKinds&ContentAudio == 0 {
		return request, "", true
	}
	stripped, changed, err := stripResponsesChatUnsupported(request.Body())
	if err != nil || !changed {
		return request, "", err == nil
	}
	effective, err := ParseCanonicalRequest(
		request.InboundProtocol(),
		request.ResponsesPath(),
		request.RequestedModel(),
		profile.Stream,
		stripped,
	)
	if err != nil {
		return request, "", false
	}
	return effective, ResponsesChatDroppedUnsupportedFields, true
}

func stripResponsesChatUnsupported(body []byte) ([]byte, bool, error) {
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, false, err
	}
	rewritten, changed := stripResponsesChatValue(document)
	if !changed {
		return body, false, nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(rewritten); err != nil {
		return nil, false, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true, nil
}

func stripResponsesChatValue(value any) (any, bool) {
	switch current := value.(type) {
	case map[string]any:
		if audioContentPart(current) {
			return nil, true
		}
		changed := false
		for _, key := range []string{"previous_response_id", "prompt_cache_key", "cache_control"} {
			if _, ok := current[key]; ok {
				delete(current, key)
				changed = true
			}
		}
		for key, child := range current {
			next, childChanged := stripResponsesChatValue(child)
			if !childChanged {
				continue
			}
			changed = true
			if next == nil {
				delete(current, key)
				continue
			}
			current[key] = next
		}
		return current, changed
	case []any:
		changed := false
		kept := make([]any, 0, len(current))
		for _, child := range current {
			next, childChanged := stripResponsesChatValue(child)
			if childChanged {
				changed = true
			}
			if next == nil && childChanged {
				continue
			}
			kept = append(kept, next)
		}
		if !changed {
			return current, false
		}
		return kept, true
	default:
		return value, false
	}
}

func audioContentPart(part map[string]any) bool {
	raw, _ := part["type"].(string)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "audio", "input_audio":
		return true
	default:
		return false
	}
}
