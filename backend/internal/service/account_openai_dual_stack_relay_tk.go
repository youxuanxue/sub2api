package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
)

// IsOpenAIDualStackClaudeRelay reports OpenAI apikey accounts whose upstream is a
// dual-stack relay (native /v1/messages + /v1/chat/completions) such as CloudWise
// MaaS or agent.tokensea.ai.
func (a *Account) IsOpenAIDualStackClaudeRelay() bool {
	if a == nil || !a.IsOpenAI() || a.Type != AccountTypeAPIKey {
		return false
	}
	if !openai_compat.ShouldUseNativeAnthropicMessagesAPI(a.Extra) {
		return false
	}
	return a.IsOpenAICloudwiseRelay() || a.IsOpenAITokenseaRelay()
}

func (a *Account) openAIDualStackRelaySupportsModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	switch {
	case a.IsOpenAICloudwiseRelay():
		_, ok := supportedOpenAICloudwiseRelayCatalogModels[model]
		return ok
	case a.IsOpenAITokenseaRelay():
		_, ok := supportedOpenAITokenseaRelayCatalogModels[model]
		return ok
	default:
		return false
	}
}

// ResolveOpenAIResponsesChatFallbackBillingModel undoes group messages-dispatch
// claude→gpt remapping for dual-stack relay accounts. Handler-level
// tkApplyResponsesDispatchModelMapping runs before account selection, so
// /v1/responses bodies may carry gpt-5.6-* while OpsModelKey still holds the
// client's claude family name. Chat-fallback relays must forward the claude ID
// the upstream actually serves.
func (a *Account) ResolveOpenAIResponsesChatFallbackBillingModel(c *gin.Context, bodyModel string) string {
	bodyModel = strings.TrimSpace(bodyModel)
	if bodyModel == "" || !a.IsOpenAIDualStackClaudeRelay() {
		return bodyModel
	}
	clientModel := bodyModel
	if c != nil {
		if opsModel := strings.TrimSpace(c.GetString(OpsModelKey)); opsModel != "" {
			clientModel = opsModel
		}
	}
	if clientModel == bodyModel {
		return bodyModel
	}
	if claudeMessagesDispatchFamily(clientModel) == "" {
		return bodyModel
	}
	if !a.openAIDualStackRelaySupportsModel(clientModel) {
		return bodyModel
	}
	return clientModel
}
