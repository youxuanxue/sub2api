package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
)

// newAPIBridgeOwnsAnthropicMessages reports whether inbound /v1/messages for
// this account must relay through the NewAPI adaptor bridge.
//
// The OpenAI-shaped fallbacks below (native Anthropic endpoint / raw Chat
// Completions) resolve their upstream from OpenAI-family accessors, which
// return "" for platform=newapi — and every caller turns "" into
// api.openai.com. A newapi channel credential (DashScope, Ark, ...) sent
// there comes back as "Incorrect API key provided". Bridge-eligible newapi
// accounts therefore stay on the adaptor, which reads the account's own
// channel base URL and key.
func (s *OpenAIGatewayService) newAPIBridgeOwnsAnthropicMessages(account *Account) bool {
	if account == nil || account.Platform != PlatformNewAPI {
		return false
	}
	// Qianfan Token Plan Person exposes a dedicated Anthropic root; do not send
	// Claude traffic through the BaiduV2 adaptor (which only has /v2/chat).
	if isNewAPIQianfanTokenPlanAccount(account) {
		return false
	}
	return s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions)
}

// tkTryRouteForwardAsAnthropic handles TokenKey-specific /v1/messages entry
// routing before the OpenAI Responses compat path.
func (s *OpenAIGatewayService) tkTryRouteForwardAsAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, bool, error) {
	// Bridge-eligible newapi accounts are relayed by ForwardAsAnthropicDispatched;
	// never claim them for an OpenAI-shaped fallback.
	if s.newAPIBridgeOwnsAnthropicMessages(account) {
		result, err := s.ForwardAsAnthropicDispatched(ctx, c, account, body, "", defaultMappedModel)
		return result, true, err
	}
	if isNewAPIQianfanTokenPlanAccount(account) {
		result, err := s.forwardAnthropicViaNativeAnthropicEndpoint(ctx, c, account, body, defaultMappedModel)
		return result, true, err
	}
	if account.IsAnthropicProtocol() || account.IsAdaptiveAPIProtocol() {
		result, err := s.forwardAnthropicViaNativeAnthropicEndpoint(ctx, c, account, body, defaultMappedModel)
		return result, true, err
	}
	if account.IsCNProvider() && account.GetAPIProtocol() == APIProtocolChatCompletions {
		result, err := s.forwardAnthropicViaRawChatCompletions(ctx, c, account, body, defaultMappedModel)
		return result, true, err
	}
	if account.IsOpenAITokenseaRelay() && shouldForwardNativeAnthropicMessagesForModel(body) {
		result, err := s.forwardAnthropicViaNativeMessages(ctx, c, account, body, defaultMappedModel)
		return result, true, err
	}
	if account.Type == AccountTypeAPIKey && !account.IsCNProvider() && openai_compat.ShouldUseNativeAnthropicMessagesAPI(account.Extra) {
		if shouldForwardNativeAnthropicMessagesForModel(body) {
			result, err := s.forwardAnthropicViaNativeMessages(ctx, c, account, body, defaultMappedModel)
			return result, true, err
		}
		result, err := s.forwardAnthropicViaRawChatCompletions(ctx, c, account, body, defaultMappedModel)
		return result, true, err
	}
	if account.Type == AccountTypeAPIKey && !account.IsCNProvider() && !openai_compat.ShouldUseResponsesAPI(account.Extra) {
		result, err := s.forwardAnthropicViaRawChatCompletions(ctx, c, account, body, defaultMappedModel)
		return result, true, err
	}
	return nil, false, nil
}

type tkAnthropicCodexOAuthTransformResult struct {
	responsesBody   []byte
	upstreamModel   string
	promptCacheKey  string
	compatTurnState string
	isStream        bool
}

// tkApplyAnthropicCodexOAuthToResponsesBody applies ChatGPT/Codex OAuth transform
// for Anthropic /v1/messages → Responses forwarding.
func (s *OpenAIGatewayService) tkApplyAnthropicCodexOAuthToResponsesBody(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	responsesBody []byte,
	upstreamModel string,
	promptCacheKey string,
	compatTurnState string,
	originalModel string,
	normalizedModel string,
	billingModel string,
) (tkAnthropicCodexOAuthTransformResult, error) {
	out := tkAnthropicCodexOAuthTransformResult{
		responsesBody:   responsesBody,
		upstreamModel:   upstreamModel,
		promptCacheKey:  promptCacheKey,
		compatTurnState: compatTurnState,
	}
	if !account.IsOpenAIOAuthLike() || account.Platform == PlatformGrok {
		return out, nil
	}
	var reqBody map[string]any
	if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
		return out, fmt.Errorf("unmarshal for codex transform: %w", err)
	}
	codexResult := applyCodexOAuthTransformWithOptions(reqBody, codexOAuthTransformOptions{
		SkipDefaultInstructions: true,
		PreserveToolCallIDs:     true,
	})
	if codexResult.Error != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", codexResult.Error.Error())
		return out, codexResult.Error
	}
	setCodexToolNameReverse(c, codexResult.ToolNameReverse)
	forcedTemplateText := ""
	if s.cfg != nil {
		forcedTemplateText = s.cfg.Gateway.ForcedCodexInstructionsTemplate
	}
	templateUpstreamModel := upstreamModel
	if codexResult.NormalizedModel != "" {
		templateUpstreamModel = codexResult.NormalizedModel
	}
	existingInstructions, _ := reqBody["instructions"].(string)
	if strings.TrimSpace(existingInstructions) == "" {
		existingInstructions = extractPromptLikeInstructionsFromInput(reqBody)
	}
	if _, err := applyForcedCodexInstructionsTemplate(reqBody, forcedTemplateText, forcedCodexInstructionsTemplateData{
		ExistingInstructions: strings.TrimSpace(existingInstructions),
		OriginalModel:        originalModel,
		NormalizedModel:      normalizedModel,
		BillingModel:         billingModel,
		UpstreamModel:        templateUpstreamModel,
	}); err != nil {
		return out, err
	}
	ensureCodexOAuthInstructionsField(reqBody)
	if shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
		appendOpenAICompatClaudeCodeTodoGuardToRequestBody(reqBody)
	}
	if codexResult.NormalizedModel != "" {
		out.upstreamModel = codexResult.NormalizedModel
	}
	if codexResult.PromptCacheKey != "" {
		out.promptCacheKey = codexResult.PromptCacheKey
	}
	delete(reqBody, "prompt_cache_key")
	if shouldAutoInjectPromptCacheKeyForCompat(out.upstreamModel) {
		out.compatTurnState = s.getOpenAICompatSessionTurnState(ctx, c, account, out.promptCacheKey)
	}
	out.isStream = true
	remarshaled, err := json.Marshal(reqBody)
	if err != nil {
		return out, fmt.Errorf("remarshal after codex transform: %w", err)
	}
	out.responsesBody = remarshaled
	return out, nil
}

// tkApplyAnthropicMessagesUpstreamHeaders applies session isolation and Codex OAuth
// header normalization for Anthropic /v1/messages upstream requests.
func tkApplyAnthropicMessagesUpstreamHeaders(
	c *gin.Context,
	account *Account,
	upstreamReq *http.Request,
	promptCacheKey string,
	apiKeyID int64,
	compatTurnState string,
) {
	if account.Platform != PlatformGrok && promptCacheKey != "" {
		isolatedSessionID := generateSessionUUID(isolateOpenAISessionID(apiKeyID, promptCacheKey))
		upstreamReq.Header.Set("session_id", isolatedSessionID)
		if upstreamReq.Header.Get("conversation_id") != "" {
			upstreamReq.Header.Set("conversation_id", isolatedSessionID)
		}
	}
	if account.IsOpenAIOAuthLike() && account.Platform != PlatformGrok {
		upstreamReq.Header.Del("OpenAI-Beta")
	}
	if account.IsOpenAIOAuthLike() && promptCacheKey != "" && strings.TrimSpace(c.GetHeader("conversation_id")) == "" {
		upstreamReq.Header.Del("conversation_id")
	}
	if compatTurnState != "" && upstreamReq.Header.Get("x-codex-turn-state") == "" {
		upstreamReq.Header.Set("x-codex-turn-state", compatTurnState)
	}
}

// tkResolveAnthropicCompatPromptCacheKey seeds prompt_cache_key for Grok and
// auto-inject compat paths on Anthropic /v1/messages forwarding.
func tkResolveAnthropicCompatPromptCacheKey(
	c *gin.Context,
	account *Account,
	body []byte,
	anthropicReq *apicompat.AnthropicRequest,
	upstreamModel string,
	promptCacheKey string,
) (string, bool) {
	if promptCacheKey == "" && account.Platform == PlatformGrok {
		if sessionSeed := extractClaudeCodeSessionID(c, body); sessionSeed != "" {
			return sessionSeed, true
		}
		if sessionSeed := promptCacheKeyFromAnthropicMetadataSession(anthropicReq); sessionSeed != "" {
			return sessionSeed, true
		}
	}
	return promptCacheKey, false
}

type tkOpenAICompatMessagesSessionPrep struct {
	promptCacheKey              string
	compatPromptCacheInjected   bool
	anthropicDigestChain        string
	anthropicMatchedDigestChain string
	compatReplayTrimmed         bool
	compatReplayTrimmedByPolicy bool
	compatReplayGuardEnabled    bool
	compatCompactionPolicy      openAICompatMessagesCompactionPolicy
	compatContinuationEnabled   bool
	previousResponseID          string
	compatContinuationDisabled  bool
	compatTurnState             string
}

// tkPrepareOpenAICompatMessagesSession applies TokenKey prompt-cache seeding,
// continuation lookup, and messages-compaction replay guard before Anthropic→
// Responses conversion.
func (s *OpenAIGatewayService) tkPrepareOpenAICompatMessagesSession(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	anthropicReq *apicompat.AnthropicRequest,
	anthropicDigestReq *apicompat.AnthropicRequest,
	upstreamModel string,
	promptCacheKey string,
	apiKeyID int64,
) tkOpenAICompatMessagesSessionPrep {
	out := tkOpenAICompatMessagesSessionPrep{promptCacheKey: strings.TrimSpace(promptCacheKey)}
	// Grok is outside the gpt-5/codex compat injector, but Claude Code still
	// carries a stable session id. Prefer that as the Grok prompt-cache seed so
	// multi-turn /v1/messages traffic can hit xAI's server-side cache.
	if out.promptCacheKey == "" && account.Platform == PlatformGrok {
		if seededKey, injected := tkResolveAnthropicCompatPromptCacheKey(c, account, body, anthropicReq, upstreamModel, out.promptCacheKey); injected {
			out.promptCacheKey = seededKey
			out.compatPromptCacheInjected = true
		}
	}
	if out.promptCacheKey == "" && shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
		out.promptCacheKey = promptCacheKeyFromAnthropicMetadataSession(anthropicReq)
		if out.promptCacheKey == "" {
			out.promptCacheKey = deriveAnthropicCacheControlPromptCacheKey(anthropicReq)
		}
		if out.promptCacheKey == "" {
			out.anthropicDigestChain = buildOpenAICompatAnthropicDigestChain(anthropicDigestReq)
			if reusedKey, matchedChain := s.findOpenAICompatAnthropicDigestPromptCacheKey(account, apiKeyID, out.anthropicDigestChain); reusedKey != "" {
				out.promptCacheKey = reusedKey
				out.anthropicMatchedDigestChain = matchedChain
			} else {
				out.promptCacheKey = promptCacheKeyFromAnthropicDigest(out.anthropicDigestChain)
			}
		}
		out.compatPromptCacheInjected = out.promptCacheKey != ""
	}
	out.compatReplayGuardEnabled = shouldAutoInjectPromptCacheKeyForCompat(upstreamModel)
	out.compatCompactionPolicy = resolveOpenAICompatMessagesCompactionPolicy(account, apiKeyGroup(getAPIKeyFromContext(c)))
	out.compatContinuationEnabled = openAICompatContinuationEnabled(account, upstreamModel)
	if out.compatContinuationEnabled {
		out.previousResponseID = s.getOpenAICompatSessionResponseID(ctx, c, account, out.promptCacheKey)
	}
	out.compatContinuationDisabled = out.compatContinuationEnabled &&
		s.isOpenAICompatSessionContinuationDisabled(ctx, c, account, out.promptCacheKey)
	if out.compatReplayGuardEnabled && shouldEvaluateOpenAICompatMessagesCompactionForAccount(account, out.previousResponseID, out.compatContinuationDisabled) {
		if shouldApplyOpenAICompatMessagesCompaction(out.compatCompactionPolicy, anthropicReq) {
			out.compatReplayTrimmed = applyOpenAICompatMessagesCompaction(account, anthropicReq)
			out.compatReplayTrimmedByPolicy = out.compatReplayTrimmed
		}
	}
	SetOpenAICompatMessagesOpsContext(c, OpenAICompatMessagesOpsSnapshot{
		PreviousResponseIDAttached: out.previousResponseID != "",
		MessagesCompactionApplied:  out.compatReplayTrimmedByPolicy,
		EstimatedInputTokens:       estimateAnthropicRequestInputTokens(anthropicReq),
	})
	return out
}

// isOpenAICompatResponsesTerminalEvent reports whether an OpenAI Responses SSE
// event type should close the Anthropic-compat streaming loop.
func isOpenAICompatResponsesTerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	// TK: align with openAIStreamEventIsTerminal so cancelled/canceled close
	// the streaming loop instead of falling through. See Wei-Shaw/sub2api#1322.
	case "response.completed", "response.done", "response.incomplete", "response.failed",
		"response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}
