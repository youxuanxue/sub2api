package service

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyVolcKimiRawChatCompletionsResponse shapes Volc Agent Plan Kimi chat JSON
// onto the Moonshot-compatible public wire: mirror usage.cached_tokens from
// prompt_tokens_details when the outer field is absent. Stream finish_reason
// null-fill lives on the SSE line path.
func applyVolcKimiRawChatCompletionsResponse(account *Account, model string, body []byte) []byte {
	if !shouldShapeVolcKimiChatResponse(account, model) || !gjson.ValidBytes(body) {
		return body
	}
	return mirrorVolcKimiOuterCachedTokens(body)
}

// applyVolcKimiRawChatCompletionsSSELine fills missing choices[].finish_reason
// with JSON null on intermediate chunks, and mirrors outer cached_tokens on
// usage-bearing frames. Terminal non-null finish_reason values are unchanged.
func applyVolcKimiRawChatCompletionsSSELine(account *Account, model string, line string) string {
	if !shouldShapeVolcKimiChatResponse(account, model) {
		return line
	}
	payload, ok := extractOpenAISSEDataLine(line)
	if !ok || payload == "[DONE]" || !gjson.Valid(payload) {
		return line
	}
	updated := ensureVolcKimiStreamFinishReasonNull([]byte(payload))
	updated = mirrorVolcKimiOuterCachedTokens(updated)
	if string(updated) == payload {
		return line
	}
	return line[:len(line)-len(payload)] + string(updated)
}

func shouldShapeVolcKimiChatResponse(account *Account, model string) bool {
	return isNewAPIVolcEngineAgentPlanAccount(account) && isKimiModel(model)
}

// mirrorVolcKimiOuterCachedTokens copies prompt_tokens_details.cached_tokens to
// usage.cached_tokens when the outer field is missing. Does not invent cache
// hits: absent details leave the body unchanged; an explicit details 0 becomes
// outer 0. Existing outer values are left alone.
func mirrorVolcKimiOuterCachedTokens(body []byte) []byte {
	usage := gjson.GetBytes(body, "usage")
	if !usage.IsObject() {
		return body
	}
	if usage.Get("cached_tokens").Exists() {
		return body
	}
	details := usage.Get("prompt_tokens_details.cached_tokens")
	if !details.Exists() || details.Type != gjson.Number {
		return body
	}
	updated, err := sjson.SetBytes(body, "usage.cached_tokens", details.Int())
	if err != nil {
		return body
	}
	return updated
}

// ensureVolcKimiStreamFinishReasonNull writes finish_reason:null onto every
// choices[] entry that omits the field. Matches Moonshot SSE examples so
// strict clients that require the key do not treat omission as a protocol
// break. Existing null or terminal string values are unchanged.
func ensureVolcKimiStreamFinishReasonNull(body []byte) []byte {
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() || len(choices.Array()) == 0 {
		return body
	}
	updated := body
	for i, choice := range choices.Array() {
		if choice.Get("finish_reason").Exists() {
			continue
		}
		path := "choices." + strconv.Itoa(i) + ".finish_reason"
		next, err := sjson.SetBytes(updated, path, nil)
		if err != nil {
			return body
		}
		updated = next
	}
	return updated
}
