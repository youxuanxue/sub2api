package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ErrGeminiNativeStreamTruncated 标记 Gemini 原生 SSE 流在任何终止事件之前结束。
var ErrGeminiNativeStreamTruncated = errors.New("upstream Gemini stream ended before any terminal event")

// geminiNativeStreamTruncatedMessage 是 Gemini 原生透传路径上游截断的 ops 消息。
const geminiNativeStreamTruncatedMessage = "Upstream Gemini stream ended before any terminal event"

// GeminiNativeStreamTruncatedCode 是对客与 ops 共用的稳定错误分类。
const GeminiNativeStreamTruncatedCode = "upstream_stream_truncated"

// geminiNativeStreamTerminalState 记录 Gemini 原生 SSE 流是否收到过**终止事件**。
//
// 背景与 openai_raw_stream_truncation.go 的头注释同源：原生透传路径此前只要上游
// 干净 EOF 就按成功收尾，上游在发出带 finishReason 的最终 chunk 之前被切断会被
// 伪装成 `HTTP 200 + usage 0/0`——客户端拿到半截回答且 SDK 不抛错，网关既不记
// ops_upstream_error 也不计 SLA，断流账号继续按健康参与候选评分与会话黏性。
//
// 四种终止信号任一出现即认为上游"讲完了"，不作截断处理：
//
//   - finishReason  —— 首候选给出任何非空枚举（STOP / MAX_TOKENS / SAFETY / ...）
//   - blockReason   —— promptFeedback 整体拦截，生成前即终止
//   - error 信封    —— 流内 Google 错误事件，已由 finalizeGeminiSSESignal 归因
//   - [DONE]        —— 兼容上游沿用 OpenAI 哨兵
//
// MAX_TOKENS 与 SAFETY/RECITATION 等都是**模型侧生成结果**而非上游故障：它们是
// 合法终止信号，守卫必须静默放行，账号归因由 gemini_response_signal.go 单独决定。
type geminiNativeStreamTerminalState struct {
	// sawDataEvent 表示上游至少发过一个可解析的 data 事件；零 data 事件的空流
	// 由 finalizeGeminiSSESignal 登记 EMPTY_STREAM，不走截断判定。
	sawDataEvent bool
	sawTerminal  bool
}

// ObserveDone 记录 [DONE] 哨兵。Gemini 原生 wire 不使用它，但兼容上游会发。
func (t *geminiNativeStreamTerminalState) ObserveDone() {
	if t == nil {
		return
	}
	t.sawTerminal = true
}

// ObserveEvent 从单个 Gemini 响应事件中提取终止信号。payload 需已解包
// （Code Assist / v1internal 信封在调用侧剥离）。
func (t *geminiNativeStreamTerminalState) ObserveEvent(payload []byte) {
	if t == nil {
		return
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return
	}
	t.sawDataEvent = true
	if t.sawTerminal || !gjson.ValidBytes(payload) {
		return
	}
	if inner := gjson.GetBytes(payload, "response"); inner.Exists() && inner.IsObject() {
		payload = []byte(inner.Raw)
	}
	if errObj := gjson.GetBytes(payload, "error"); errObj.Exists() && errObj.IsObject() {
		t.sawTerminal = true
		return
	}
	if blockReason := strings.TrimSpace(gjson.GetBytes(payload, "promptFeedback.blockReason").String()); blockReason != "" {
		t.sawTerminal = true
		return
	}
	// 任何非空 finishReason 都是终止：模型侧枚举（MAX_TOKENS / SAFETY / ...）同样算
	// 上游讲完了，不得判为截断。
	if strings.TrimSpace(geminiPrimaryCandidate(payload).Get("finishReason").String()) != "" {
		t.sawTerminal = true
	}
}

// IsTruncated 判定上游是否在任何终止信号之前结束。
func (t *geminiNativeStreamTerminalState) IsTruncated() bool {
	return t != nil && t.sawDataEvent && !t.sawTerminal
}

// geminiNativeStreamClientAborted 判定下游已先行离开：取消会连带取消上游请求，
// 此时上游读到的 EOF 与真实截断不可区分，沿用既有语义不判为上游故障。
func geminiNativeStreamClientAborted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	err := ctx.Err()
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// recordGeminiNativeStreamTruncation 把上游截断记入 ops 上下文，并返回终止错误。
//
// 字节已经写给客户端了，所以按 anthropic-buffered-stream-failure-contract.md 的
// "已有可用内容后未完整 EOF" 一行处理：返回普通终止错误（不是 UpstreamFailoverError），
// 不重放——重放会让客户端收到重复内容——同时标记 stream_truncated 并计入 SLA。
func recordGeminiNativeStreamTruncation(
	c *gin.Context,
	account *Account,
	upstreamRequestID string,
) error {
	if c == nil {
		return ErrGeminiNativeStreamTruncated
	}
	platform := PlatformGemini
	accountID := int64(0)
	accountName := ""
	if account != nil {
		platform = account.Platform
		accountID = account.ID
		accountName = account.Name
	}

	setOpsUpstreamError(c, http.StatusBadGateway, geminiNativeStreamTruncatedMessage, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           platform,
		AccountID:          accountID,
		AccountName:        accountName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               "stream_truncated",
		Reason:             GeminiNativeStreamTruncatedCode,
		Message:            geminiNativeStreamTruncatedMessage,
	})
	MarkOpsStreamErrorValue(c, OpsStreamError{
		ErrType:         "upstream_error",
		Code:            GeminiNativeStreamTruncatedCode,
		Message:         geminiNativeStreamTruncatedMessage,
		IntendedStatus:  http.StatusBadGateway,
		CountTowardsSLA: true,
	})
	return ErrGeminiNativeStreamTruncated
}

// finalizeAntigravityGeminiStreamTruncation 是 Antigravity 原生 Gemini 透传路径的
// 截断收尾。守卫判定住在 geminiNativeStreamTerminalState，归因住在
// recordGeminiNativeStreamTruncation；本函数只做"事实 → 错误"的单一接线，是
// antigravityStreamResult.truncated 的唯一消费者。
func finalizeAntigravityGeminiStreamTruncation(
	c *gin.Context,
	res *antigravityStreamResult,
	account *Account,
	upstreamRequestID string,
) error {
	if res == nil || !res.truncated || res.clientDisconnect {
		return nil
	}
	if c != nil && c.Request != nil && geminiNativeStreamClientAborted(c.Request.Context()) {
		return nil
	}
	return recordGeminiNativeStreamTruncation(c, account, upstreamRequestID)
}
