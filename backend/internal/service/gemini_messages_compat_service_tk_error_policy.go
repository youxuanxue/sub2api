package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// geminiErrorPolicyClientWrite selects the client error envelope used when
// ErrorPolicySkipped cannot failover (Claude Messages wire vs native Gemini).
type geminiErrorPolicyClientWrite int

const (
	geminiErrorPolicyClientClaude geminiErrorPolicyClientWrite = iota
	geminiErrorPolicyClientNative
)

// tkApplyGeminiErrorPolicy owns the duplicated CheckErrorPolicy switch shared by
// Gemini Messages (Claude wire) and native ForwardNative paths.
//
// Returns (nil, false) when the caller should continue with ErrorPolicyNone
// handling (nil rateLimitService, Antigravity relay capacity, or ErrorPolicyNone).
// Returns (err, true) when a policy branch fully handled the response.
func (s *GeminiMessagesCompatService) tkApplyGeminiErrorPolicy(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	resp *http.Response,
	respBody []byte,
	upstreamRequestID string,
	isOAuth bool,
	clientWrite geminiErrorPolicyClientWrite,
) (err error, handled bool) {
	if s.rateLimitService == nil ||
		tkIsAntigravityRelayCapacityResponse(account, resp.StatusCode, respBody) {
		return nil, false
	}
	switch s.rateLimitService.CheckErrorPolicy(ctx, account, resp.StatusCode, respBody) {
	case ErrorPolicySkipped:
		if failoverErr := s.skippedErrorPolicyFailoverError(c, account, resp.StatusCode, respBody, upstreamRequestID); failoverErr != nil {
			return failoverErr, true
		}
		if account.IsCustomErrorCodesEnabled() {
			switch clientWrite {
			case geminiErrorPolicyClientClaude:
				return s.writeGeminiCustomCodeSkippedError(c, account, resp.StatusCode, upstreamRequestID, respBody, func() {
					_ = s.writeClaudeError(c, http.StatusInternalServerError, "api_error", geminiCustomCodeSkippedClientMessage)
				}), true
			default:
				return s.writeGeminiCustomCodeSkippedError(c, account, resp.StatusCode, upstreamRequestID, respBody, func() {
					_ = s.writeGoogleError(c, http.StatusInternalServerError, geminiCustomCodeSkippedClientMessage)
				}), true
			}
		}
		// 池模式：客户端写出与 ErrorPolicyNone 相同，仅跳过账号状态标记。
		switch clientWrite {
		case geminiErrorPolicyClientClaude:
			return s.writeGeminiMappedError(c, account, resp.StatusCode, upstreamRequestID, respBody), true
		default:
			return s.writeGeminiNativeUpstreamError(c, account, resp, respBody, upstreamRequestID, isOAuth), true
		}
	case ErrorPolicyMatched, ErrorPolicyTempUnscheduled:
		s.handleGeminiUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		evBody := respBody
		if clientWrite == geminiErrorPolicyClientNative {
			evBody = unwrapIfNeeded(isOAuth, respBody)
		}
		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(evBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		upstreamDetail := ""
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = truncateString(string(evBody), maxBytes)
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  upstreamRequestID,
			Kind:               "failover",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		return newUpstreamFailoverErrorWithTKCapacity(account, resp.StatusCode, resp.Header, respBody), true
	}
	return nil, false
}

// checkErrorPolicyInLoop 在重试循环内预检查错误策略。
// 返回 true 表示策略已匹配（调用者应 break），resp 已重建可直接使用。
// 返回 false 表示 ErrorPolicyNone，resp 已重建，调用者继续走重试逻辑。
func (s *GeminiMessagesCompatService) checkErrorPolicyInLoop(
	ctx context.Context,
	account *Account,
	resp *http.Response,
	requestedModel string,
) (matched bool, rebuilt *http.Response) {
	if resp.StatusCode < 400 {
		return false, resp
	}
	body := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	rebuilt = &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	if strings.TrimSpace(requestedModel) != "" &&
		tkIsAntigravityRelayCapacityResponse(account, resp.StatusCode, body) {
		if s.rateLimitService != nil {
			s.rateLimitService.handleAntigravityRelayCapacity(
				ctx,
				account,
				resp.StatusCode,
				body,
				requestedModel,
			)
		}
		return true, rebuilt
	}
	if s.rateLimitService == nil {
		return false, rebuilt
	}
	policy := s.rateLimitService.CheckErrorPolicy(ctx, account, resp.StatusCode, body)
	return policy != ErrorPolicyNone, rebuilt
}

// skippedErrorPolicyFailoverError 命中 ErrorPolicySkipped（池模式、或自定义错误码未命中）
// 时构造 failover 错误：可 failover 的状态码返回 UpstreamFailoverError，交给 handler 层换号
// （池模式账号按 pool_mode_retry_count 先同账号重试）；返回 nil 表示状态码不可 failover，
// 由调用方决定客户端写出。Skipped 只豁免账号状态标记，不豁免换号，与 OpenAI 网关路径一致。
func (s *GeminiMessagesCompatService) skippedErrorPolicyFailoverError(c *gin.Context, account *Account, statusCode int, respBody []byte, upstreamRequestID string) *UpstreamFailoverError {
	if !s.shouldFailoverGeminiUpstreamError(statusCode) {
		return nil
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	upstreamDetail := s.upstreamErrorDetail(respBody)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  upstreamRequestID,
		Kind:               "failover",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	return &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           respBody,
		RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode),
	}
}
