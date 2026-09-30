package service

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// tkBridgeUpstreamShouldFailoverAfterPenalty reports whether a bridge upstream
// error should trigger the handler's existing failedAccountIDs failover loop
// after any account-level penalty has been applied. Account-standing failures
// qualify, as do gateway outage statuses that are safe to retry on another
// provider without mutating account state. Client-induced 400 and model-not-found
// 404 stay terminal on non-NVIDIA accounts to avoid draining the pool (#617).
// Opaque provider 404s wrapped as bad_response_status_code still failover.
//
// NVIDIA Build is treated as unstable secondary capacity: any upstream error
// from those accounts fails over so volcengine/qianfan siblings can absorb
// provider blips (prod 2026-09-29 glm-5.3-flash opaque 404 storm).
func tkBridgeUpstreamShouldFailoverAfterPenalty(account *Account, apiErr *newapitypes.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	if isNewAPINVIDIABuildAccount(account) {
		return true
	}
	return classifyGatewayFailover(gatewayFailoverObservation{
		Profile:    gatewayFailoverProfileNewAPIBridge,
		Semantic:   tkBridgeFailureSemantic(apiErr),
		StatusCode: apiErr.StatusCode,
	}).RetryNextAccount
}

func tkBridgeFailureSemantic(apiErr *newapitypes.NewAPIError) gatewayFailureSemantic {
	if tkIsBridgeUpstreamArrears(apiErr) {
		return gatewayFailureSemanticAccountFault
	}
	if apiErr != nil && isUpstreamModelRetiredError(apiErr.StatusCode, tkBridgeUpstreamErrorBody(apiErr), tkBridgeUpstreamRelayMessage(apiErr)) {
		return gatewayFailureSemanticAccountFault
	}
	// NVIDIA Build (and similar relays) sometimes surface an opaque
	// bad_response_status_code 404 with no model-not-found prose. That is a
	// provider/path blip, not a client model-id fault: fail over to siblings
	// (volcengine/qianfan) instead of returning final 404 (#glm-flash-404).
	if tkIsBridgeOpaqueBadResponse404(apiErr) {
		return gatewayFailureSemanticTransientFault
	}
	if apiErr != nil {
		if upstream, ok := tkBridgeUpstreamOpenAIError(apiErr); ok && tkSupplierThinkingToolPreflight(apiErr.StatusCode, upstream.Message) {
			// A supplier preflight rejected this model/feature combination before
			// execution. Another endpoint may satisfy the original request.
			return gatewayFailureSemanticTransientFault
		}
	}
	if apiErr != nil && apiErr.StatusCode == http.StatusInternalServerError {
		if upstream, ok := tkBridgeUpstreamOpenAIError(apiErr); ok {
			message := strings.ToLower(strings.TrimSpace(tkBridgeDecodeSupplierMessage(upstream.Message)))
			message, _, _ = strings.Cut(message, " (request id:")
			// These envelopes describe the supplier's own unavailable upstream,
			// not invalid client input or evidence that our credential is revoked.
			switch message {
			case "upstream access forbidden, please contact administrator", "没有可用账号，请稍后重试":
				return gatewayFailureSemanticTransientFault
			}
		}
	}
	return gatewayFailureSemanticUnclassified
}

// tkIsBridgeOpaqueBadResponse404 reports a provider-opaque HTTP 404 wrapped as
// bad_response_status_code without model-not-found / InvalidEndpointOrModel
// diagnostics. True model-not-found shapes stay terminal (#617). Body parsing
// delegates to isUpstreamOpaqueProvider404 (shared with model cooldown).
func tkIsBridgeOpaqueBadResponse404(apiErr *newapitypes.NewAPIError) bool {
	if apiErr == nil || apiErr.StatusCode != http.StatusNotFound {
		return false
	}
	body := tkBridgeUpstreamErrorBody(apiErr)
	msg := tkBridgeUpstreamRelayMessage(apiErr)
	if IsOpenAICompatModelNotFound404(body, msg) {
		return false
	}
	if isUpstreamOpaqueProvider404(apiErr.StatusCode, body) {
		return true
	}
	// Thin fallback when body synthesis is empty but the NewAPIError code/message
	// still carries the opaque relay wrapper.
	code := strings.ToLower(strings.TrimSpace(string(apiErr.GetErrorCode())))
	combined := strings.ToLower(strings.TrimSpace(msg))
	if code == "bad_response_status_code" {
		return true
	}
	return strings.Contains(combined, "bad response status code 404")
}

func tkSupplierThinkingToolPreflight(status int, message string) bool {
	return status == http.StatusBadRequest && strings.HasPrefix(strings.TrimSpace(message), "[preflight:R3.forced_tool_choice_incompatible]")
}

func (s *OpenAIGatewayService) failoverNativeMessagesUpstreamHTTPError(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, body []byte, message, model string) *UpstreamFailoverError {
	if rejection := candidateEdgeModelRejection(ctx, account, resp.StatusCode, resp.Header, body, model); rejection != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			Kind: "failover", Message: message,
		})
		return rejection
	}
	if account == nil || account.Platform != PlatformNewAPI || !tkSupplierThinkingToolPreflight(resp.StatusCode, message) {
		return s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, body, message, model)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
		Kind: "failover", Message: message,
	})
	return applyGatewayFailoverSemantic(&UpstreamFailoverError{
		StatusCode: resp.StatusCode, ResponseBody: body, RequestScopedTransient: true,
	}, gatewayFailoverProfileNewAPIBridge, gatewayFailureSemanticTransientFault)
}

// Some relays encode UTF-8 error bytes as Latin-1 characters in their JSON.
func tkBridgeDecodeSupplierMessage(message string) string {
	decoded := make([]byte, 0, len(message))
	for _, r := range message {
		if r > 255 {
			return message
		}
		decoded = append(decoded, byte(r))
	}
	if utf8.Valid(decoded) {
		return string(decoded)
	}
	return message
}

func tkNewAPIBridgeUpstreamFailoverError(c *gin.Context, account *Account, apiErr *newapitypes.NewAPIError) *UpstreamFailoverError {
	statusCode := http.StatusBadGateway
	var body []byte
	if apiErr != nil {
		statusCode = apiErr.StatusCode
		body = tkBridgeUpstreamErrorBody(apiErr)
		if c != nil {
			TkRecordBridgeUpstreamError(c, statusCode, apiErr)
		}
	}
	semantic := tkBridgeFailureSemantic(apiErr)
	if isNewAPINVIDIABuildAccount(account) {
		// NVIDIA failures are request-scoped: cool the model when possible, but
		// always treat the attempt as retryable on a sibling account.
		semantic = gatewayFailureSemanticTransientFault
	} else if semantic == gatewayFailureSemanticUnclassified {
		semantic = gatewayFailureSemanticAccountFault
	}
	return applyGatewayFailoverSemantic(&UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           body,
		RequestScopedTransient: semantic == gatewayFailureSemanticTransientFault,
	}, gatewayFailoverProfileNewAPIBridge, semantic)
}

func bridgeWrapRelayErrorAfterPenalty(
	ctx context.Context,
	rls *RateLimitService,
	c *gin.Context,
	account *Account,
	apiErr *newapitypes.NewAPIError,
) error {
	// Model retirement / opaque provider 404 is scoped to the executed Plan,
	// not the whole account. The legacy account-penalty allowlist deliberately
	// excludes these statuses so they do not SetError the credential.
	if rls != nil && apiErr != nil && (isUpstreamModelRetiredError(apiErr.StatusCode, tkBridgeUpstreamErrorBody(apiErr)) ||
		tkIsBridgeOpaqueBadResponse404(apiErr)) {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		rls.HandleUpstreamModelNotFound(stateCtx, account, protocolExecutionResolvedModel(ctx, ""), apiErr.StatusCode, tkBridgeUpstreamErrorBody(apiErr))
	}
	// Soft-deprioritize unstable NVIDIA Build so the next selection prefers
	// siblings instead of repeating a failover hop. Does not SetError.
	if rls != nil && apiErr != nil && isNewAPINVIDIABuildAccount(account) {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		rls.recordNVIDIABuildInstability(stateCtx, account.ID, apiErr.StatusCode)
		cancel()
	}
	tkHandleBridgeUpstreamPenalty(ctx, rls, account, apiErr)
	if tkBridgeUpstreamShouldFailoverAfterPenalty(account, apiErr) {
		return tkNewAPIBridgeUpstreamFailoverError(c, account, apiErr)
	}
	return tkWrapBridgeRelayError(c, apiErr)
}
