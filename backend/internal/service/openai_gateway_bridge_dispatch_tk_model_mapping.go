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
	_, upstreamModel := resolveOpenAICompatForwardModels(account, originalModel, defaultMappedModel)
	rewritten := body
	modelForNorm := originalModel
	if upstreamModel != "" && upstreamModel != originalModel {
		rewritten = ReplaceModelInBody(body, upstreamModel)
		modelForNorm = upstreamModel
	} else if upstreamModel != "" {
		modelForNorm = upstreamModel
	}
	if normalizedBody, normalized := NormalizeGLMOpenAIReasoningEffort(rewritten, modelForNorm); normalized {
		return normalizedBody
	}
	return rewritten
}
