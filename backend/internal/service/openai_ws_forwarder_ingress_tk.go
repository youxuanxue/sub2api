package service

import (
	"context"
	"encoding/json"

	"github.com/gin-gonic/gin"
)

// tkPrepareWSIngressClientPayload applies TokenKey normalization before model
// mapping on inbound WS v2 response.create frames.
func (s *OpenAIGatewayService) tkPrepareWSIngressClientPayload(
	c *gin.Context,
	account *Account,
	normalized []byte,
	hooks *OpenAIWSIngressHooks,
) ([]byte, error) {
	if hooks != nil && (hooks.MaxReasoningEffort != "" || len(hooks.ReasoningEffortMappings) > 0) {
		if capped, changed := ApplyOpenAIReasoningEffortPolicy(normalized, hooks.MaxReasoningEffort, hooks.ReasoningEffortMappings); changed {
			normalized = capped
		}
	}
	if compatibilityBody, compatibilityChanged, compatibilityErr := normalizeOpenAIResponsesWebSocketCompatibilityBody(normalized, account); compatibilityErr != nil {
		return nil, compatibilityErr
	} else if compatibilityChanged {
		normalized = compatibilityBody
	}
	if account.IsOpenAIOAuthLike() {
		aliasedBody, reverse, aliased, aliasErr := aliasOpenAIOAuthReservedToolNamesBody(normalized)
		if aliasErr != nil {
			return nil, aliasErr
		}
		updateCodexToolNameReverseForWSFrame(c, normalized, reverse)
		if aliased {
			normalized = aliasedBody
		}
	}
	return normalized, nil
}

// tkApplyCodexWSIngressImageBridge injects Codex image-generation bridge tools and
// instructions when enabled for Codex CLI websocket ingress.
func (s *OpenAIGatewayService) tkApplyCodexWSIngressImageBridge(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	isCodexCLI bool,
	normalized []byte,
) ([]byte, error) {
	if account.IsOpenAIOAuthLike() && isOpenAIResponsesLiteWebSocketPayload(normalized) {
		litePayload, _, liteErr := normalizeOpenAIResponsesLiteToolsPayload(normalized)
		if liteErr != nil {
			return nil, liteErr
		}
		normalized = litePayload
	}
	apiKey := getAPIKeyFromContext(c)
	imageGenerationAllowed := GroupAllowsImageGeneration(apiKeyGroup(apiKey))
	codexImageGenerationExplicitToolPolicy := codexImageGenerationExplicitToolPolicyAllow
	if isCodexCLI {
		codexImageGenerationExplicitToolPolicy = account.CodexImageGenerationExplicitToolPolicy()
	}
	codexBridgeEnabled := isCodexCLI &&
		!isOpenAIResponsesLiteWebSocketPayload(normalized) &&
		imageGenerationAllowed &&
		codexImageGenerationExplicitToolPolicy != codexImageGenerationExplicitToolPolicyStrip &&
		s.isCodexImageGenerationBridgeEnabled(ctx, account, apiKey)
	if !codexBridgeEnabled {
		return normalized, nil
	}
	payloadMap := make(map[string]any)
	if err := decodeOpenAIJSONUseNumber(normalized, &payloadMap); err != nil {
		return nil, err
	}
	bridgeModified := false
	if ensureOpenAIResponsesImageGenerationTool(payloadMap) {
		bridgeModified = true
		logOpenAIWSModeInfo("ingress_ws_codex_image_tool_injected account_id=%d", account.ID)
	}
	if ensureOpenAIResponsesImageGenerationToolChoiceAuto(payloadMap) {
		bridgeModified = true
		logOpenAIWSModeInfo("ingress_ws_codex_image_tool_choice_auto account_id=%d", account.ID)
	}
	if normalizeOpenAIResponsesImageGenerationTools(payloadMap) {
		bridgeModified = true
	}
	if applyCodexImageGenerationBridgeInstructions(payloadMap) {
		bridgeModified = true
		logOpenAIWSModeInfo("ingress_ws_codex_image_bridge_instructions_added account_id=%d", account.ID)
	}
	if !bridgeModified {
		return normalized, nil
	}
	return json.Marshal(payloadMap)
}
