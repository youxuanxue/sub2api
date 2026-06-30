package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// TK: See upstream Wei-Shaw/sub2api#3158 — WS v2 passthrough bypasses the Codex
// image_generation bridge that the regular ingress path applies in
// openai_ws_forwarder.go. Passthrough must inject/normalize the same tool shape
// before forwarding response.create frames upstream.

func (s *OpenAIGatewayService) applyCodexImageBridgeToWSResponseCreate(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	payload []byte,
) ([]byte, error) {
	if s == nil || account == nil || len(payload) == 0 {
		return payload, nil
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "response.create" {
		return payload, nil
	}

	isCodexCLI := false
	if c != nil {
		isCodexCLI = openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator"))
	}
	if s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		isCodexCLI = true
	}
	if !isCodexCLI {
		return payload, nil
	}

	apiKey := getAPIKeyFromContext(c)
	if !GroupAllowsImageGeneration(apiKeyGroup(apiKey)) {
		return payload, nil
	}
	if !s.isCodexImageGenerationBridgeEnabled(ctx, account, apiKey) {
		return payload, nil
	}

	payloadMap := make(map[string]any)
	if err := json.Unmarshal(payload, &payloadMap); err != nil {
		return payload, err
	}

	bridgeModified := false
	if ensureOpenAIResponsesImageGenerationTool(payloadMap) {
		bridgeModified = true
		logOpenAIWSV2Passthrough("ingress_ws_passthrough_codex_image_tool_injected account_id=%d", account.ID)
	}
	if ensureOpenAIResponsesImageGenerationToolChoiceAuto(payloadMap) {
		bridgeModified = true
		logOpenAIWSV2Passthrough("ingress_ws_passthrough_codex_image_tool_choice_auto account_id=%d", account.ID)
	}
	if normalizeOpenAIResponsesImageGenerationTools(payloadMap) {
		bridgeModified = true
	}
	if applyCodexImageGenerationBridgeInstructions(payloadMap) {
		bridgeModified = true
		logOpenAIWSV2Passthrough("ingress_ws_passthrough_codex_image_bridge_instructions_added account_id=%d", account.ID)
	}

	rebuilt := payload
	if bridgeModified {
		next, marshalErr := json.Marshal(payloadMap)
		if marshalErr != nil {
			return payload, marshalErr
		}
		rebuilt = next
	}

	originalModel := strings.TrimSpace(gjson.GetBytes(rebuilt, "model").String())
	upstreamModel := normalizeOpenAIModelForUpstream(account, account.GetMappedModel(originalModel))
	if stripped, changed, stripErr := stripCodexSparkImageGenerationToolFromRawPayload(rebuilt, upstreamModel); stripErr != nil {
		return payload, stripErr
	} else if changed {
		rebuilt = stripped
		logOpenAIWSV2Passthrough("ingress_ws_passthrough_codex_spark_image_tool_stripped account_id=%d", account.ID)
	}

	return rebuilt, nil
}
