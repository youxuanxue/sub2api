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

// finalErrorPolicy 返回循环外处理最终上游错误响应时使用的错误策略。
// 每个上游响应只检查一次：CheckErrorPolicy 有副作用（触发临时不可调度规则，
// 并据账号上已记录的 401 暂停把 401 升级为 error），对同一响应检查两次会让一次
// 401 既临时停调度又升级成 error。
// checked 非 nil 时它就是重试循环内对同一响应的结果，直接复用。
// 调用方保证 s.rateLimitService 非 nil。
//
// requestedModel 决定隔离粒度（空=账号级），各路径必须传本路径原先在循环外用的
// 那个值，循环内预检查也传同一个，否则复用的结果与原语义不符。scope 语义本身仍停在
// _upstream_deferred_tests/temp_unsched_scope_test.go，待 TK 吸收后统一处理。
func (s *GeminiMessagesCompatService) finalErrorPolicy(
	ctx context.Context,
	account *Account,
	statusCode int,
	body []byte,
	requestedModel string,
	checked *ErrorPolicyResult,
) ErrorPolicyResult {
	if checked != nil {
		return *checked
	}
	if requestedModel == "" {
		return s.rateLimitService.CheckErrorPolicy(ctx, account, statusCode, body)
	}
	return s.rateLimitService.CheckErrorPolicy(ctx, account, statusCode, body, requestedModel)
}

// tkApplyGeminiErrorPolicy owns the duplicated CheckErrorPolicy switch shared by
// Gemini Messages (Claude wire) and native ForwardNative paths.
//
// Returns (nil, false) when the caller should continue with ErrorPolicyNone
// handling (nil rateLimitService, Antigravity relay capacity, or ErrorPolicyNone).
// Returns (err, true) when a policy branch fully handled the response.
//
// checkedPolicy 非 nil 时就是重试循环内对同一响应的检查结果，直接复用，不再重查。
func (s *GeminiMessagesCompatService) tkApplyGeminiErrorPolicy(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	resp *http.Response,
	respBody []byte,
	upstreamRequestID string,
	isOAuth bool,
	clientWrite geminiErrorPolicyClientWrite,
	checkedPolicy *ErrorPolicyResult,
	requestedModel string,
) (err error, handled bool) {
	if s.rateLimitService == nil {
		return nil, false
	}
	policy := s.finalErrorPolicy(ctx, account, resp.StatusCode, respBody, requestedModel, checkedPolicy)
	switch policy {
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
		// TempUnscheduled 表示管理员配置的临时不可调度规则已经裁决完这个响应，
		// 账号状态处理到此为止。再调 handleGeminiUpstreamError 会二次处理同一个
		// 上游错误：401 于是既临时停调度、又被 ratelimit 的 401 阶梯升级为 error，
		// 等于把「给 token 刷新留一个窗口」直接跳过。Matched（自定义错误码命中）
		// 才需要默认账号状态处理。
		if policy == ErrorPolicyMatched {
			s.handleGeminiUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		}
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
		return newUpstreamFailoverError(resp.StatusCode, resp.Header, respBody), true
	}
	return nil, false
}

// checkErrorPolicyInLoop 在重试循环内预检查错误策略。
// 返回 true 表示策略已匹配（调用者应 break），resp 已重建可直接使用。
// 返回 false 表示 ErrorPolicyNone，resp 已重建，调用者继续走重试逻辑。
//
// checked 回传本次检查结果（未检查时为 nil），调用方必须把它交给
// tkApplyGeminiErrorPolicy 复用：CheckErrorPolicy 有副作用（触发
// temp_unschedulable 规则，并据账号上已记录的 401 暂停把 401 升级为 error），
// 对同一个上游响应检查两次会让一次 401 既临时停调度又升级成 error。
func (s *GeminiMessagesCompatService) checkErrorPolicyInLoop(
	ctx context.Context,
	account *Account,
	resp *http.Response,
	requestedModel string,
) (matched bool, rebuilt *http.Response, checked *ErrorPolicyResult) {
	if resp.StatusCode < 400 {
		return false, resp, nil
	}
	body := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	rebuilt = &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	if s.rateLimitService == nil {
		return false, rebuilt, nil
	}
	// requestedModel 保持不传给 CheckErrorPolicy：传了会把命中从账号级暂停降级成
	// 模型级冷却（scope 语义仍停在 _upstream_deferred_tests/temp_unsched_scope_test.go）。
	policy := s.rateLimitService.CheckErrorPolicy(ctx, account, resp.StatusCode, body)
	return policy != ErrorPolicyNone, rebuilt, &policy
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
		RetryableOnSameAccount: !tkIsAccountCapacityFailure(account, statusCode, "", respBody) && account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode),
	}
}
