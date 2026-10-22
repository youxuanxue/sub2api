package service

// TK-only SSE stream error failover: maps mid-stream event:error frames to failover errors.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// sseStreamErrorFailover 把「上游 HTTP 200 + SSE 流体内 event:error 帧」转成一个
// failover 错误，并补全 ops 上下文。
//
// TK: 普通流内错误返回 502（Bad Gateway）而非 403。mid-stream 上游挂掉的语义是「上游服务不可用」，
// 不是 Forbidden —— 用 403 会把它塞进 handle403 的「封号 / WAF / 额度」判定空间
// (#810/#831/#832)，并曾与 failover_loop 对 empty-body 403 的 fast-fail 相撞
// (commit c7785a7e)。502 与 stream-read-error 路径一致、走 TempUnscheduleRetryableError
// 的 empty-response 钩子。上游新增的类型化检测 + ops 日志 + ResponseBody 一并保留。
func (s *GatewayService) sseStreamErrorFailover(c *gin.Context, account *Account, resp *http.Response, sseErr *sseStreamErrorEventError, statusCode int) *UpstreamFailoverError {
	body := []byte(sseErr.RawData)

	upstreamMsg := sanitizeUpstreamErrorMessage(
		strings.TrimSpace(extractUpstreamErrorMessage(body)),
	)

	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(sseErr.RawData, maxBytes)
	}

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               "stream_error",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	logger.LegacyPrintf("service.gateway",
		"[Forward] SSE error event in stream: Account=%d(%s) RequestID=%s Body=%s",
		account.ID, account.Name, resp.Header.Get("x-request-id"),
		truncateString(sseErr.RawData, 1000),
	)

	return &UpstreamFailoverError{
		StatusCode:   statusCode,
		ResponseBody: body,
	}
}

// sseStreamErrorEventFailover 把兼容流转发（chat/completions、responses）里
// 上游 HTTP 200 + SSE event:error 转成 UpstreamFailoverError。
//
// 原生 /v1/messages 继续走 sseStreamErrorFailover，流内错误保持 502，避免 403 进入
// 封号 / WAF / empty-body fast-fail。兼容路径与上游一致：尚未写出内容且错误类型为
// overloaded_error 时用 529 并触发账号侧冷却，其余用 403。403 的响应体是结构化 JSON，
// failover 循环不会把它当成空 body 直接放弃换号。
func (s *GatewayService) sseStreamErrorEventFailover(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, rawData, model string, outputStarted bool) *UpstreamFailoverError {
	body := []byte(rawData)
	semanticStatus := http.StatusForbidden
	if !outputStarted && gjson.GetBytes(body, "error.type").String() == "overloaded_error" {
		semanticStatus = 529
		if s.rateLimitService != nil && resp != nil {
			header := http.Header{}
			if resp.Header != nil {
				header = resp.Header.Clone()
			}
			syntheticResp := &http.Response{
				StatusCode: semanticStatus,
				Header:     header,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}
			s.handleFailoverSideEffects(ctx, syntheticResp, account, model)
		}
	}
	return s.sseStreamErrorFailover(c, account, resp, &sseStreamErrorEventError{RawData: rawData}, semanticStatus)
}
