package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 引用缓存不存在（含已过期、已删除、不属于当前项目）是**请求**问题，不是账号问题。
//
// 判定器 isGeminiCachedContentNotFound 早已存在，但 2026-10-22 上游合并把调用点
// 丢在了 TK 改写过的转发路径外：403/400/404 于是落进通用账号错误处理，表现为
//   - 换号（UpstreamFailoverError）：换一个账号依然读不到同一份缓存，纯属浪费；
//   - 标记账号错误 / 命中 temp_unschedulable 规则：一个客户端拿着失效 cache id
//     重试，就能把整池健康账号逐个打成不可调度；
//   - Vertex 的 400/404 还会被当成「模型不存在」记进模型限流。
//
// 这两个入口是该语义的唯一 owner：转发路径只调用它们，不要复制状态码/文案判断。

// geminiCachedContentPassthroughInLoop 判断上游响应是否应跳过同号重试与错误策略，
// 直接进入客户端写出：引用缓存不存在，或绑定缓存的请求在上游网关账号上收到的
// 429 / 5xx（那反映的是持有缓存的上游账号，不是本账号）。
//
// 响应体读过一次就没了，所以用内存副本重建 resp 交还调用方。
func (s *GeminiMessagesCompatService) geminiCachedContentPassthroughInLoop(
	ctx context.Context,
	account *Account,
	resp *http.Response,
) (passthrough bool, rebuilt *http.Response) {
	if resp == nil {
		return false, resp
	}
	if isGeminiBoundCacheUpstreamPassthrough(ctx, account, resp.StatusCode) {
		return true, resp
	}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
	default:
		return false, resp
	}
	body := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	rebuilt = &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	return isGeminiCachedContentNotFound(resp.StatusCode, body), rebuilt
}

// writeGeminiCachedContentNotFound 记录上游原始错误供 ops 诊断，然后对客户端统一
// 返回 AI Studio 形态的 403：上游的 Vertex 400/404 与缓存数字 ID 都不外泄。
// 按请求级错误（permission_error）统计，所以不计入账号/平台错误率。
func (s *GeminiMessagesCompatService) writeGeminiCachedContentNotFound(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	respBody []byte,
	requestID string,
	isOAuth bool,
) error {
	respBody = unwrapIfNeeded(isOAuth, respBody)
	upstreamMsg := s.recordGeminiCachedContentUpstreamError(c, account, resp, respBody, requestID)
	MarkOpsRequestScopedError(c, "permission_error")
	MarkResponseCommitted(c)
	c.Data(http.StatusForbidden, "application/json", []byte(GeminiCachedContentNotFoundResponse))
	if upstreamMsg == "" {
		return fmt.Errorf("gemini upstream error: %d", resp.StatusCode)
	}
	return fmt.Errorf("gemini upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

// recordGeminiCachedContentUpstreamError 只登记 ops 诊断，不写客户端响应。
func (s *GeminiMessagesCompatService) recordGeminiCachedContentUpstreamError(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	respBody []byte,
	requestID string,
) string {
	upstreamMsg := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))
	upstreamDetail := s.upstreamErrorDetail(respBody)
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  requestID,
		Kind:               "http_error",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	return upstreamMsg
}
