package service

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// allowAnthropicThinkingContract400Repair mirrors GatewayService.shouldRectifySignatureError
// for OpenAI-gateway native Anthropic paths so admin rectifier switches stay honored.
func (s *OpenAIGatewayService) allowAnthropicThinkingContract400Repair(
	ctx context.Context,
	account *Account,
	respBody []byte,
	mappedModel string,
) bool {
	if !ShouldApplyRetryFilters(mappedModel) {
		return false
	}
	if !isAnthropicThinkingContractErrorMessage(extractUpstreamErrorMessage(respBody)) {
		return false
	}
	if s == nil || s.settingService == nil {
		// Tests / misconfig: keep repair available when settings are absent.
		return true
	}
	if account != nil && account.Type == AccountTypeAPIKey {
		settings, err := s.settingService.GetRectifierSettings(ctx)
		return err == nil && settings.Enabled && settings.APIKeySignatureEnabled
	}
	return s.settingService.IsSignatureRectifierEnabled(ctx)
}

func closeHTTPResponseBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// retryAnthropicThinkingContract400HTTP runs the shared cannot-be-modified /
// signature 400 ladder (rectify → optional signature-sensitive escalate).
// send must POST an already-prepared body and return the upstream response.
// readErr should match readOpenAIUpstreamError (consume, close, restore Body).
//
// Callers must not Close resp.Body before calling — pass the response after
// readOpenAIUpstreamError has restored a readable Body.
func (s *OpenAIGatewayService) retryAnthropicThinkingContract400HTTP(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	mappedModel string,
	wireBody []byte,
	resp *http.Response,
	respBody []byte,
	upstreamMsg string,
	send func(body []byte) (*http.Response, error),
	readErr func(*http.Response) ([]byte, string),
) (outResp *http.Response, outWire []byte, outBody []byte, outMsg string, recovered bool) {
	outResp, outWire, outBody, outMsg = resp, wireBody, respBody, upstreamMsg
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		return outResp, outWire, outBody, outMsg, false
	}
	if !s.allowAnthropicThinkingContract400Repair(ctx, account, respBody, mappedModel) {
		return outResp, outWire, outBody, outMsg, false
	}
	repaired, kind, ok := tkRectifyAnthropicThinkingContract400(wireBody, mappedModel, respBody)
	if !ok {
		return outResp, outWire, outBody, outMsg, false
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: http.StatusBadRequest,
		Kind:               kind,
		Message:            extractUpstreamErrorMessage(respBody),
	})
	retryResp, retryErr := send(repaired)
	if retryErr != nil || retryResp == nil {
		return outResp, outWire, outBody, outMsg, false
	}
	closeHTTPResponseBody(outResp)
	outResp = retryResp
	outWire = repaired
	if outResp.StatusCode < 400 {
		return outResp, outWire, nil, "", true
	}
	outBody, outMsg = readErr(outResp)

	if outResp.StatusCode == http.StatusBadRequest &&
		kind == "thinking_cannot_modify_strip_historical" &&
		isAnthropicThinkingCannotBeModifiedError(outBody) {
		escalated := FilterSignatureSensitiveBlocksForRetry(outWire, mappedModel)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: http.StatusBadRequest,
			Kind:               "thinking_cannot_modify_signature_sensitive",
			Message:            extractUpstreamErrorMessage(outBody),
		})
		escResp, escErr := send(escalated)
		if escErr != nil || escResp == nil {
			return outResp, outWire, outBody, outMsg, false
		}
		closeHTTPResponseBody(outResp)
		outResp = escResp
		outWire = escalated
		if outResp.StatusCode < 400 {
			return outResp, outWire, nil, "", true
		}
		outBody, outMsg = readErr(outResp)
	}
	return outResp, outWire, outBody, outMsg, false
}
