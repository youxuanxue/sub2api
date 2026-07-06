package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// rewriteNewAPIBridgeBodyModel applies account-level model_mapping (including TK
// dated GLM alias via resolveOpenAIForwardModel) before the newapi adaptor reads
// body.model. Non-bridge paths already rewrite upstream model; bridge dispatch
// previously forwarded the client id verbatim and upstreams like DashScope reject
// VolcEngine dated SKUs such as glm-4-7-251222.
func rewriteNewAPIBridgeBodyModel(account *Account, body []byte, defaultMappedModel string) []byte {
	originalModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if originalModel == "" || account == nil {
		return body
	}
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	if upstreamModel == "" || upstreamModel == originalModel {
		return body
	}
	rewritten := ReplaceModelInBody(body, upstreamModel)
	if normalizedBody, normalized := NormalizeGLMOpenAIReasoningEffort(rewritten, upstreamModel); normalized {
		return normalizedBody
	}
	return rewritten
}
