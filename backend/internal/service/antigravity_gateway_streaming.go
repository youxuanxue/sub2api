package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

type antigravityStreamResult struct {
	usage            *ClaudeUsage
	firstTokenMs     *int
	clientDisconnect bool // 客户端是否在流式传输过程中断开
	// truncated 表示上游在任何终止事件之前干净 EOF。判定与归因都归
	// gemini_native_stream_truncation_tk.go 所有，这里只传递事实。
	truncated bool
	// modelBytesWritten 表示客户端已经收到可用输出（非空正文、工具或媒体）。
	// 只缓冲过签名、空白 text 或 thought 时为 false，截断仍可换号。
	modelBytesWritten bool
}

func (s *AntigravityGatewayService) observeAntigravityGeminiSSELine(c *gin.Context, line string) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	// Observe the original payload: ObserveGemini supports both the v1internal
	// wrapper and direct Gemini response shapes. The main stream handler will
	// unwrap the same line for business processing, so unwrapping here would be
	// duplicate work on every SSE event.
	raw := []byte(payload)
	observer.ObserveGemini(raw)
	// Image accounting needs the inner Gemini shape (candidates.*.parts); the
	// v1internal envelope hides them under response.*.
	if inner, err := s.unwrapV1InternalResponse(raw); err == nil {
		observeGeminiImageOutputs(c, inner)
	} else {
		observeGeminiImageOutputs(c, raw)
	}
}

// antigravityClientWriter 封装流式响应的客户端写入，自动检测断开并标记。
// 断开后所有写入操作变为 no-op，调用方通过 Disconnected() 判断是否继续 drain 上游。
type antigravityClientWriter struct {
	w                gin.ResponseWriter
	flusher          http.Flusher
	disconnected     bool
	prefix           string // 日志前缀，标识来源方法
	beforeFirstWrite func()
}

func newAntigravityClientWriter(w gin.ResponseWriter, flusher http.Flusher, prefix string) *antigravityClientWriter {
	return &antigravityClientWriter{w: w, flusher: flusher, prefix: prefix}
}

// Write 写入数据到客户端，写入失败时标记断开并返回 false
func (cw *antigravityClientWriter) Write(p []byte) bool {
	if cw.disconnected {
		return false
	}
	cw.prepareFirstWrite()
	if _, err := cw.w.Write(p); err != nil {
		cw.markDisconnected()
		return false
	}
	cw.flusher.Flush()
	return true
}

// Fprintf 格式化写入数据到客户端，写入失败时标记断开并返回 false
func (cw *antigravityClientWriter) Fprintf(format string, args ...any) bool {
	if cw.disconnected {
		return false
	}
	cw.prepareFirstWrite()
	if _, err := fmt.Fprintf(cw.w, format, args...); err != nil {
		cw.markDisconnected()
		return false
	}
	cw.flusher.Flush()
	return true
}

func (cw *antigravityClientWriter) Disconnected() bool { return cw.disconnected }

func (cw *antigravityClientWriter) prepareFirstWrite() {
	if cw.beforeFirstWrite == nil {
		return
	}
	prepare := cw.beforeFirstWrite
	cw.beforeFirstWrite = nil
	prepare()
}

func (cw *antigravityClientWriter) markDisconnected() {
	cw.disconnected = true
	logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during streaming (%s), continuing to drain upstream for billing", cw.prefix)
}

// handleStreamReadError 处理上游读取错误的通用逻辑。
// 返回 (clientDisconnect, handled)：handled=true 表示错误已处理，调用方应返回已收集的 usage。
func handleStreamReadError(err error, clientDisconnected bool, prefix string) (disconnect bool, handled bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		logger.LegacyPrintf("service.antigravity_gateway", "Context canceled during streaming (%s), returning collected usage", prefix)
		return true, true
	}
	if clientDisconnected {
		logger.LegacyPrintf("service.antigravity_gateway", "Upstream read error after client disconnect (%s): %v, returning collected usage", prefix, err)
		return true, true
	}
	return false, false
}

func (s *AntigravityGatewayService) handleGeminiStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time) (*antigravityStreamResult, error) {
	if upstreamResponseModelObserverFromContext(c) == nil {
		beginUpstreamResponseModelObservation(c)
	}
	c.Status(resp.StatusCode)
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream; charset=utf-8"
	}
	c.Header("Content-Type", contentType)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	// 使用 Scanner 并限制单行大小，避免 ReadString 无上限导致 OOM
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.settingService.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	scanner.Split(newBoundedSSESplit(s.settingService.cfg, maxLineSize, nil))
	usage := &ClaudeUsage{}
	var firstTokenMs *int
	var terminal geminiNativeStreamTerminalState

	type scanEvent struct {
		line string
		err  error
	}
	// 独立 goroutine 读取上游，避免读取阻塞影响超时处理
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	queueBudget := newSSEQueueBudget(s.settingService.cfg)
	sendEvent := func(ev scanEvent) bool {
		if !queueBudget.acquire(len(ev.line), done) {
			return false
		}
		select {
		case events <- ev:
			return true
		case <-done:
			queueBudget.release(len(ev.line))
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	// 上游数据间隔超时保护（防止上游挂起长期占用连接）
	streamInterval := time.Duration(0)
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// 下游 keepalive：防止代理/Cloudflare Tunnel 因连接空闲而断开
	keepaliveInterval := time.Duration(0)
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.settingService.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	// Gemini CLI / @google/genai processStreamResponse only drains "data:" frames.
	// A ":\n\n" comment sticks in that buffer and throws
	// "Incomplete JSON segment at the end" at EOF. gemini-4-argon thinking gaps
	// exceed the keepalive interval, so the comment used to land mid-stream.
	// This path always emits the whitespace frame. go-genai / python-genai still
	// get no heartbeat: they reject any non-"data:" event, and a bare newline
	// glues onto the next event and fails their prefix check.
	keepaliveFrame := geminiNativeSSEKeepaliveFrame
	if keepaliveInterval > 0 && downstreamRejectsSSEComments(c) {
		keepaliveInterval = 0
	}
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()
	lastWroteDataEvent := false

	cw := newAntigravityClientWriter(c.Writer, flusher, "antigravity gemini")
	// 签名、空白 text、thought 先扣住。出现可用输出或终态时按原顺序刷出；
	// 流在此之前截断则整段丢弃，调用方换号。扣住期间不刷新 lastDataAt，
	// 让已有空白心跳继续发给客户端。
	var held []string
	heldBytes := 0
	modelBytesWritten := false
	flushHeld := func() {
		for _, item := range held {
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			if !cw.Fprintf("data: %s\n\n", item) {
				break
			}
			lastDataAt = time.Now()
			lastWroteDataEvent = true
			modelBytesWritten = true
		}
		held = nil
		heldBytes = 0
	}
	writeModelPayload := func(payload string) {
		if len(held) > 0 {
			flushHeld()
		}
		if cw.Disconnected() {
			return
		}
		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if !cw.Fprintf("data: %s\n\n", payload) {
			return
		}
		lastDataAt = time.Now()
		lastWroteDataEvent = true
		modelBytesWritten = true
	}

	// 仅发送一次错误事件，避免多次写入导致协议混乱
	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent || cw.Disconnected() {
			return
		}
		errorEventSent = true
		_, _ = fmt.Fprint(c.Writer, geminiNativeSSEErrorFrame(http.StatusBadGateway, reason))
		flusher.Flush()
	}

	for {
		select {
		case ev, ok := <-events:
			if ok {
				queueBudget.release(len(ev.line))
			}
			if !ok {
				return &antigravityStreamResult{
					usage:             usage,
					firstTokenMs:      firstTokenMs,
					clientDisconnect:  cw.Disconnected(),
					truncated:         terminal.IsTruncated(),
					modelBytesWritten: modelBytesWritten,
				}, nil
			}
			if ev.err != nil {
				if disconnect, handled := handleStreamReadError(ev.err, cw.Disconnected(), "antigravity gemini"); handled {
					return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: disconnect}, nil
				}
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity): max_size=%d error=%v", maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return nil, ev.err
			}

			line := ev.line
			s.observeAntigravityGeminiSSELine(c, line)
			trimmed := strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				// Drop empty keepalives and OpenAI-style [DONE]. Gemini CLI /
				// @google/genai requires every SSE event to end with \n\n; writing
				// "data: [DONE]\n" (single newline) leaves residual buffer and
				// throws "Incomplete JSON segment at the end" when the stream
				// closes. Native Gemini wire format does not use [DONE].
				if payload == "" || payload == "[DONE]" {
					if payload == "[DONE]" {
						terminal.ObserveDone()
					}
					continue
				}

				// 解包 v1internal 响应
				inner, parseErr := s.unwrapV1InternalResponse([]byte(payload))
				if parseErr == nil && inner != nil {
					payload = string(inner)
				}
				terminal.ObserveEvent(inner)

				// 解析 usage
				if u := extractGeminiUsage(inner); u != nil {
					usage = u
				}
				var parsed map[string]any
				if json.Unmarshal(inner, &parsed) == nil {
					// Check for MALFORMED_FUNCTION_CALL
					if candidates, ok := parsed["candidates"].([]any); ok && len(candidates) > 0 {
						if cand, ok := candidates[0].(map[string]any); ok {
							if fr, ok := cand["finishReason"].(string); ok && fr == "MALFORMED_FUNCTION_CALL" {
								logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] MALFORMED_FUNCTION_CALL detected in forward stream")
								if content, ok := cand["content"]; ok {
									if b, err := json.Marshal(content); err == nil {
										logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] Malformed content: %s", string(b))
									}
								}
							}
						}
					}
				}

				if !modelBytesWritten && inner != nil && geminiNativeEventHoldable(inner) && heldBytes+len(payload) <= geminiNativeHeldPrefixLimit {
					held = append(held, payload)
					heldBytes += len(payload)
					continue
				}
				writeModelPayload(payload)
				continue
			}

			// Upstream follows each data event with a blank separator. The data
			// branch already wrote "data: ...\n\n"; re-emitting that blank as a
			// keepalive "\n" yields "\n\n\n" and breaks google-genai framing.
			// A held prefix does not count as a written data event, so its
			// separator is forwarded as the whitespace keepalive. Standalone
			// empty lines before first content use the same frame.
			if trimmed == "" {
				if lastWroteDataEvent {
					lastWroteDataEvent = false
					continue
				}
				// A held prefix has not emitted a data frame yet, so this blank
				// separator is only a keepalive. go-genai / python-genai split on
				// "\n\n" and reject a leading newline glued onto the first data
				// line; their idle ticker is already disabled for the same reason.
				if !modelBytesWritten && downstreamRejectsSSEComments(c) {
					continue
				}
				if cw.Fprintf("%s", geminiNativeSSEKeepaliveFrame) {
					lastDataAt = time.Now()
				}
				continue
			}
			lastWroteDataEvent = false

			// Preserve upstream liveness without forwarding SSE control fields.
			if isGeminiNativeSSEKeepalive(trimmed) {
				if cw.Fprintf("%s", geminiNativeSSEKeepaliveFrame) {
					lastDataAt = time.Now()
				}
			}
			continue

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if cw.Disconnected() {
				logger.LegacyPrintf("service.antigravity_gateway", "Upstream timeout after client disconnect (antigravity gemini), returning collected usage")
				return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity)")
			sendErrorEvent("stream_timeout")
			return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if cw.Disconnected() {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// Gemini-CLI-safe keepalive (whitespace only; never SSE comments).
			if !cw.Fprintf("%s", keepaliveFrame) {
				logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during keepalive ping (antigravity gemini), continuing to drain upstream for billing")
				continue
			}
			lastDataAt = time.Now()
		}
	}
}

// handleGeminiStreamToNonStreaming 读取上游流式响应，合并为非流式响应返回给客户端
// Gemini 流式响应是增量的，需要累积所有 chunk 的内容
func (s *AntigravityGatewayService) handleGeminiStreamToNonStreaming(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) (*antigravityStreamResult, error) {
	if upstreamResponseModelObserverFromContext(c) == nil {
		beginUpstreamResponseModelObservation(c)
	}
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.settingService.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	scanner.Split(newBoundedSSESplit(s.settingService.cfg, maxLineSize, nil))

	usage := &ClaudeUsage{}
	var firstTokenMs *int
	var last map[string]any
	var lastWithParts map[string]any
	var collectedParts []map[string]any
	var finishReason string

	type scanEvent struct {
		line string
		err  error
	}

	// 独立 goroutine 读取上游，避免读取阻塞影响超时处理
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	queueBudget := newSSEQueueBudget(s.settingService.cfg)
	sendEvent := func(ev scanEvent) bool {
		if !queueBudget.acquire(len(ev.line), done) {
			return false
		}
		select {
		case events <- ev:
			return true
		case <-done:
			queueBudget.release(len(ev.line))
			return false
		}
	}

	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	// 上游数据间隔超时保护（防止上游挂起长期占用连接）
	streamInterval := time.Duration(0)
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	for {
		select {
		case ev, ok := <-events:
			if ok {
				queueBudget.release(len(ev.line))
			}
			if !ok {
				// 流结束，返回收集的响应
				goto returnResponse
			}
			if ev.err != nil {
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity non-stream): max_size=%d error=%v", maxLineSize, ev.err)
				}
				return nil, ev.err
			}

			line := ev.line
			s.observeAntigravityGeminiSSELine(c, line)
			trimmed := strings.TrimRight(line, "\r\n")

			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}

			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			// 解包 v1internal 响应
			inner, parseErr := s.unwrapV1InternalResponse([]byte(payload))
			if parseErr != nil {
				continue
			}
			upstreamResponseModelObserverFromContext(c).ObserveGemini(inner)
			observeGeminiImageOutputs(c, inner)

			var parsed map[string]any
			if err := json.Unmarshal(inner, &parsed); err != nil {
				continue
			}

			// 记录首 token 时间
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}

			last = parsed

			// 提取 usage
			if u := extractGeminiUsage(inner); u != nil {
				usage = u
			}

			// Check for MALFORMED_FUNCTION_CALL
			if candidates, ok := parsed["candidates"].([]any); ok && len(candidates) > 0 {
				if cand, ok := candidates[0].(map[string]any); ok {
					if fr, ok := cand["finishReason"].(string); ok && fr != "" {
						finishReason = fr
					}
					if fr, ok := cand["finishReason"].(string); ok && fr == "MALFORMED_FUNCTION_CALL" {
						logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] MALFORMED_FUNCTION_CALL detected in forward non-stream collect")
						if content, ok := cand["content"]; ok {
							if b, err := json.Marshal(content); err == nil {
								logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] Malformed content: %s", string(b))
							}
						}
					}
				}
			}

			// 保留最后一个有 parts 的响应
			if parts := extractGeminiParts(parsed); len(parts) > 0 {
				lastWithParts = parsed
				collectedParts = append(collectedParts, parts...)
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity non-stream)")
			return nil, fmt.Errorf("stream data interval timeout")
		}
	}

returnResponse:
	// 选择最后一个有效响应
	finalResponse := pickGeminiCollectResult(last, lastWithParts)

	// 处理空响应情况 — 触发同账号重试 + failover 切换账号
	if last == nil && lastWithParts == nil {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] warning: empty stream response (gemini non-stream), triggering failover")
		return nil, &UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
			RetryableOnSameAccount: true,
		}
	}

	// TK: Preserve tools, thought signatures and ordered media via the shared collector.
	finalResponse = mergeCollectedPartsToResponse(finalResponse, collectedParts)
	// A terminal event may have no parts, and a usage-only event may follow it.
	// Keep its finish reason so callers can distinguish complete from partial images.
	if finishReason != "" {
		if candidates, ok := finalResponse["candidates"].([]any); ok && len(candidates) > 0 {
			if candidate, ok := candidates[0].(map[string]any); ok {
				candidate["finishReason"] = finishReason
			}
		}
	}

	// Complete upstream JSON with no inlineData is a content outcome, not an
	// account fault. Return a plain error so Images/native ingress fails once
	// (US059: never replay) instead of same-account failover + temp unscheduling.
	if err := requireGeminiImageModelOutput(originalModel, finalResponse); err != nil {
		body, _ := json.Marshal(finalResponse)
		setOpsUpstreamError(c, http.StatusBadGateway, err.Error(), summarizeGeminiEmptyImageBody(body))
		return nil, err
	}

	respBody, err := json.Marshal(finalResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal response: %w", err)
	}
	c.Data(http.StatusOK, "application/json", respBody)

	return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs}, nil
}

// getOrCreateGeminiParts 获取 Gemini 响应的 parts 结构，返回深拷贝和更新回调
func getOrCreateGeminiParts(response map[string]any) (result map[string]any, existingParts []any, setParts func([]any)) {
	// 深拷贝 response
	result = make(map[string]any)
	for k, v := range response {
		result[k] = v
	}

	// 获取或创建 candidates
	candidates, ok := result["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		candidates = []any{map[string]any{}}
	}

	// 获取第一个 candidate
	candidate, ok := candidates[0].(map[string]any)
	if !ok {
		candidate = make(map[string]any)
		candidates[0] = candidate
	}

	// 获取或创建 content
	content, ok := candidate["content"].(map[string]any)
	if !ok {
		content = map[string]any{"role": "model"}
		candidate["content"] = content
	}

	// 获取现有 parts
	existingParts, ok = content["parts"].([]any)
	if !ok {
		existingParts = []any{}
	}

	// 返回更新回调
	setParts = func(newParts []any) {
		content["parts"] = newParts
		result["candidates"] = candidates
	}

	return result, existingParts, setParts
}

// mergeCollectedPartsToResponse 将收集的所有 parts 合并到 Gemini 响应中
// 这个函数会合并所有类型的 parts：text、thinking、functionCall、inlineData 等
// 保持原始顺序，只合并连续的普通 text parts
func mergeCollectedPartsToResponse(response map[string]any, collectedParts []map[string]any) map[string]any {
	if len(collectedParts) == 0 {
		return response
	}

	result, _, setParts := getOrCreateGeminiParts(response)

	// 合并策略：
	// 1. 保持原始顺序
	// 2. 连续的普通 text parts 合并为一个
	// 3. thinking、functionCall、inlineData 等保持原样
	var mergedParts []any
	var textBuffer strings.Builder

	flushTextBuffer := func() {
		if textBuffer.Len() > 0 {
			mergedParts = append(mergedParts, map[string]any{
				"text": textBuffer.String(),
			})
			textBuffer.Reset()
		}
	}

	for _, part := range collectedParts {
		// 检查是否是普通 text part
		if text, ok := part["text"].(string); ok {
			// Preserve signatures and other metadata; only bare text can be coalesced.
			if len(part) > 1 {
				// thinking part，先刷新 text buffer，然后保留原样
				flushTextBuffer()
				mergedParts = append(mergedParts, part)
			} else {
				// 普通 text，累积到 buffer
				_, _ = textBuffer.WriteString(text)
			}
		} else {
			// 非 text part（functionCall、inlineData 等），先刷新 text buffer，然后保留原样
			flushTextBuffer()
			mergedParts = append(mergedParts, part)
		}
	}

	// 刷新剩余的 text
	flushTextBuffer()

	setParts(mergedParts)
	return result
}

const antigravityStatusClientClosed = StatusClientClosedRequest

func (s *AntigravityGatewayService) writeClaudeError(c *gin.Context, status int, errType, message string) error {
	MarkResponseCommitted(c)
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": message},
	})
	return fmt.Errorf("%s", message)
}

// WriteMappedClaudeError 导出版本，供 handler 层使用（如 fallback 错误处理）
func (s *AntigravityGatewayService) WriteMappedClaudeError(c *gin.Context, account *Account, upstreamStatus int, upstreamRequestID string, body []byte) error {
	return s.writeMappedClaudeError(c, account, upstreamStatus, upstreamRequestID, body)
}

func (s *AntigravityGatewayService) writeMappedClaudeError(c *gin.Context, account *Account, upstreamStatus int, upstreamRequestID string, body []byte) error {
	MarkResponseCommitted(c)
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	logBody, maxBytes := s.getLogConfig()
	upstreamDetail := s.getUpstreamErrorDetail(body)
	setOpsUpstreamError(c, upstreamStatus, upstreamMsg, upstreamDetail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: upstreamStatus,
		UpstreamRequestID:  upstreamRequestID,
		Kind:               "http_error",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	// 记录上游错误详情便于排障（可选：由配置控制；不回显到客户端）
	if logBody {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] upstream_error status=%d body=%s", upstreamStatus, truncateForLog(body, maxBytes))
	}

	// 检查错误透传规则
	if ptStatus, ptErrType, ptErrMsg, matched := applyErrorPassthroughRule(
		c, account.Platform, upstreamStatus, body,
		0, "", "",
	); matched {
		c.JSON(ptStatus, gin.H{
			"type":  "error",
			"error": gin.H{"type": ptErrType, "message": ptErrMsg},
		})
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d", upstreamStatus)
		}
		return fmt.Errorf("upstream error: %d message=%s", upstreamStatus, upstreamMsg)
	}

	var statusCode int
	var errType, errMsg string

	switch upstreamStatus {
	case 400:
		statusCode = http.StatusBadRequest
		errType = "invalid_request_error"
		errMsg = getPassthroughOrDefault(upstreamMsg, "Invalid request")
	case 404:
		statusCode = http.StatusBadRequest
		errType = "invalid_request_error"
		errMsg = "Upstream model not found"
	case 401:
		statusCode = http.StatusBadGateway
		errType = "authentication_error"
		errMsg = "Upstream authentication failed"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "permission_error"
		errMsg = "Upstream access forbidden"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = TkClientFacingBusy429Message
	case 529:
		statusCode = http.StatusServiceUnavailable
		errType = "overloaded_error"
		errMsg = "Upstream service overloaded"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	c.JSON(statusCode, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": errMsg},
	})
	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", upstreamStatus)
	}
	return fmt.Errorf("upstream error: %d message=%s", upstreamStatus, upstreamMsg)
}

func (s *AntigravityGatewayService) writeGoogleError(c *gin.Context, status int, message string) error {
	MarkResponseCommitted(c)
	statusStr := "UNKNOWN"
	switch status {
	case 400:
		statusStr = "INVALID_ARGUMENT"
	case 404:
		statusStr = "NOT_FOUND"
	case 429:
		statusStr = "RESOURCE_EXHAUSTED"
	case antigravityStatusClientClosed:
		statusStr = "CANCELLED"
	case 500:
		statusStr = "INTERNAL"
	case 502, 503:
		statusStr = "UNAVAILABLE"
	}

	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  statusStr,
		},
	})
	return fmt.Errorf("%s", message)
}

// collectClaudeStreamResponse 收集上游流式响应，转换为 Claude 非流式格式返回
// 用于处理客户端非流式请求但上游只支持流式的情况
func (s *AntigravityGatewayService) collectClaudeStreamResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) ([]byte, *antigravityStreamResult, error) {
	if upstreamResponseModelObserverFromContext(c) == nil {
		beginUpstreamResponseModelObservation(c)
	}
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.settingService.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	scanner.Split(newBoundedSSESplit(s.settingService.cfg, maxLineSize, nil))

	var firstTokenMs *int
	var last map[string]any
	var lastWithParts map[string]any
	var collectedParts []map[string]any // 收集所有 parts（包括 text、thinking、functionCall、inlineData 等）
	var meaningfulResponse bool

	type scanEvent struct {
		line string
		err  error
	}

	// 独立 goroutine 读取上游，避免读取阻塞影响超时处理
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	queueBudget := newSSEQueueBudget(s.settingService.cfg)
	sendEvent := func(ev scanEvent) bool {
		if !queueBudget.acquire(len(ev.line), done) {
			return false
		}
		select {
		case events <- ev:
			return true
		case <-done:
			queueBudget.release(len(ev.line))
			return false
		}
	}

	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	// 上游数据间隔超时保护（防止上游挂起长期占用连接）
	streamInterval := time.Duration(0)
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	for {
		select {
		case ev, ok := <-events:
			if ok {
				queueBudget.release(len(ev.line))
			}
			if !ok {
				// 流结束，转换并返回响应
				goto returnResponse
			}
			if ev.err != nil {
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity claude non-stream): max_size=%d error=%v", maxLineSize, ev.err)
				}
				return nil, nil, ev.err
			}

			line := ev.line
			trimmed := strings.TrimRight(line, "\r\n")

			if !strings.HasPrefix(trimmed, "data:") {
				continue
			}

			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			// 解包 v1internal 响应
			inner, parseErr := s.unwrapV1InternalResponse([]byte(payload))
			if parseErr != nil {
				continue
			}
			upstreamResponseModelObserverFromContext(c).ObserveGemini(inner)
			observeGeminiImageOutputs(c, inner)

			var parsed map[string]any
			if err := json.Unmarshal(inner, &parsed); err != nil {
				continue
			}

			// 最后的错误帧可能没有 parts，必须在聚合选择前保留失败原因。
			if extractGeminiFinishReason(parsed) == "MALFORMED_FUNCTION_CALL" {
				return nil, nil, antigravity.ErrMalformedFunctionCall
			}

			last = parsed

			// 保留最后一个有 parts 的响应，并收集所有 parts
			parts := extractGeminiParts(parsed)
			if len(parts) > 0 {
				lastWithParts = parsed

				// 收集所有 parts（text、thinking、functionCall、inlineData 等）
				collectedParts = append(collectedParts, parts...)
			}
			if len(parts) > 0 || strings.TrimSpace(extractGeminiFinishReason(parsed)) != "" {
				meaningfulResponse = true
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity claude non-stream)")
			return nil, nil, fmt.Errorf("stream data interval timeout")
		}
	}

returnResponse:
	// 处理空响应情况 — 触发同账号重试 + failover 切换账号
	if !meaningfulResponse {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] warning: empty stream response (claude non-stream), triggering failover")
		return nil, nil, &UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
			RetryableOnSameAccount: true,
		}
	}

	// 选择最后一个有效响应
	finalResponse := pickGeminiCollectResult(last, lastWithParts)

	// 将收集的所有 parts 合并到最终响应中
	if len(collectedParts) > 0 {
		finalResponse = mergeCollectedPartsToResponse(finalResponse, collectedParts)
	}

	// Image models must deliver inlineData; empty success would still bill when
	// ImageCount was hardcoded to 1. Fail closed before Claude/Chat conversion.
	// RequestScopedTransient: model content outcome, not account health — allow
	// account switch without temp-unscheduling the healthy account.
	if err := requireGeminiImageModelOutput(originalModel, finalResponse); err != nil {
		body, _ := json.Marshal(finalResponse)
		setOpsUpstreamError(c, http.StatusBadGateway, err.Error(), summarizeGeminiEmptyImageBody(body))
		return nil, nil, &UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			ResponseBody:           body,
			RetryableOnSameAccount: true,
			RequestScopedTransient: true,
		}
	}

	// 序列化为 JSON（Gemini 格式）
	geminiBody, err := json.Marshal(finalResponse)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal gemini response: %w", err)
	}

	// 转换 Gemini 响应为 Claude 格式
	claudeResp, agUsage, err := antigravity.TransformGeminiToClaude(geminiBody, originalModel)
	if err != nil {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] transform_error error=%v body=%s", err, string(geminiBody))
		return nil, nil, fmt.Errorf("failed to parse upstream response: %w", err)
	}

	// 转换为 service.ClaudeUsage
	usage := &ClaudeUsage{
		InputTokens:              agUsage.InputTokens,
		OutputTokens:             agUsage.OutputTokens,
		CacheCreationInputTokens: agUsage.CacheCreationInputTokens,
		CacheReadInputTokens:     agUsage.CacheReadInputTokens,
		ImageOutputTokens:        agUsage.ImageOutputTokens,
	}

	return claudeResp, &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs}, nil
}

// handleClaudeStreamToNonStreaming 收集上游流式响应，转换为 Claude 非流式格式返回
// 用于处理客户端非流式请求但上游只支持流式的情况
func (s *AntigravityGatewayService) handleClaudeStreamToNonStreaming(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) (*antigravityStreamResult, error) {
	claudeResp, streamRes, err := s.collectClaudeStreamResponse(c, resp, startTime, originalModel)
	if err != nil {
		var failoverErr *UpstreamFailoverError
		if errors.As(err, &failoverErr) {
			return nil, err
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		errMsg := "Failed to parse upstream response"
		errType := "upstream_error"
		if errors.Is(err, antigravity.ErrMalformedFunctionCall) {
			// Match Chat/Responses compat: do not collapse MALFORMED into a vague
			// upstream_error, and never surface an empty Claude 200.
			errMsg = err.Error()
			errType = "malformed_function_call"
		} else if strings.Contains(err.Error(), "stream data interval timeout") {
			errMsg = "Upstream stream data interval timeout"
			errType = "upstream_timeout"
		} else if errors.Is(err, bufio.ErrTooLong) {
			errMsg = "Upstream response line too long"
			errType = "response_too_large"
		}

		return nil, s.writeClaudeError(c, http.StatusBadGateway, errType, errMsg)
	}
	c.Data(http.StatusOK, "application/json", claudeResp)
	return streamRes, nil
}

// antigravityClaudeStreamCommit 在出现真实终态或正文/tool_use 之前，先扣住 message_start 与 thinking。
// 这样缺终态的 thinking-only 流还没有向客户端提交，调用方可以换号。
type antigravityClaudeStreamCommit struct {
	writer         *antigravityClientWriter
	held           []byte
	visibleWritten bool
	keepaliveSent  bool
}

func (c *antigravityClaudeStreamCommit) accept(p *antigravity.StreamingProcessor, events []byte, onWrite func()) {
	if c == nil || len(events) == 0 {
		return
	}
	if p.MessageStopSent() || p.HasVisibleOutput() {
		if p.HasVisibleOutput() {
			c.visibleWritten = true
		}
		c.flush(events, onWrite)
		return
	}
	c.held = append(c.held, events...)
}

func (c *antigravityClaudeStreamCommit) flush(extra []byte, onWrite func()) {
	wrote := false
	if len(c.held) > 0 {
		c.writer.Write(c.held)
		c.held = nil
		wrote = true
	}
	if len(extra) > 0 {
		c.writer.Write(extra)
		wrote = true
	}
	if wrote && onWrite != nil {
		onWrite()
	}
}

func (c *antigravityClaudeStreamCommit) committedToClient() bool {
	return c != nil && (c.visibleWritten || c.keepaliveSent)
}

// handleClaudeStreamingResponse 处理 Claude 流式响应（Gemini SSE → Claude SSE 转换）
func (s *AntigravityGatewayService) handleClaudeStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) (*antigravityStreamResult, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	processor := antigravity.NewStreamingProcessor(originalModel)
	var firstTokenMs *int
	// 使用 Scanner 并限制单行大小，避免 ReadString 无上限导致 OOM
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.settingService.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	scanner.Split(newBoundedSSESplit(s.settingService.cfg, maxLineSize, nil))

	// 辅助函数：转换 antigravity.ClaudeUsage 到 service.ClaudeUsage
	convertUsage := func(agUsage *antigravity.ClaudeUsage) *ClaudeUsage {
		if agUsage == nil {
			return &ClaudeUsage{}
		}
		return &ClaudeUsage{
			InputTokens:              agUsage.InputTokens,
			OutputTokens:             agUsage.OutputTokens,
			CacheCreationInputTokens: agUsage.CacheCreationInputTokens,
			CacheReadInputTokens:     agUsage.CacheReadInputTokens,
		}
	}

	type scanEvent struct {
		line string
		err  error
	}
	// 独立 goroutine 读取上游，避免读取阻塞影响超时处理
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	queueBudget := newSSEQueueBudget(s.settingService.cfg)
	sendEvent := func(ev scanEvent) bool {
		if !queueBudget.acquire(len(ev.line), done) {
			return false
		}
		select {
		case events <- ev:
			return true
		case <-done:
			queueBudget.release(len(ev.line))
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	streamInterval := time.Duration(0)
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// 下游 keepalive：防止代理/Cloudflare Tunnel 因连接空闲而断开
	keepaliveInterval := time.Duration(0)
	if s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.settingService.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()

	cw := newAntigravityClientWriter(c.Writer, flusher, "antigravity claude")
	commit := &antigravityClaudeStreamCommit{writer: cw}
	noteWritten := func() {
		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
	}

	// 仅发送一次错误事件，避免多次写入导致协议混乱
	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent || cw.Disconnected() {
			return
		}
		errorEventSent = true
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\":\"%s\"}\n\n", reason)
		flusher.Flush()
	}

	// finishUsage 是获取 processor 最终 usage 的辅助函数
	finishUsage := func() *ClaudeUsage {
		_, agUsage := processor.Finish()
		return convertUsage(agUsage)
	}

	for {
		select {
		case ev, ok := <-events:
			if ok {
				queueBudget.release(len(ev.line))
			}
			if !ok {
				finalEvents, agUsage := processor.Finish()
				usage := convertUsage(agUsage)
				if cw.Disconnected() {
					return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
				}
				if fault := processor.TerminalFault(); fault != antigravity.StreamTerminalNone {
					if commit.committedToClient() {
						commit.flush(nil, noteWritten)
						reason := antigravityTerminalErrorReason(fault)
						sendErrorEvent(reason)
						MarkResponseCommitted(c)
						return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs}, &antigravityStreamTerminalError{reason: reason}
					}
					return nil, antigravityMissingTerminalFailover(fault)
				}
				commit.flush(finalEvents, noteWritten)
				if !processor.MessageStartSent() {
					// 整个流未收到任何可解析的上游数据（全部 SSE 行均无法被 JSON 解析），
					// 触发 failover 在同账号重试，避免向客户端发出缺少 message_start 的残缺流
					logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Claude-Stream] empty stream response (no valid events parsed), triggering failover")
					return nil, &UpstreamFailoverError{
						StatusCode:             http.StatusBadGateway,
						ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
						RetryableOnSameAccount: true,
					}
				}
				return &antigravityStreamResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: false}, nil
			}
			if ev.err != nil {
				if disconnect, handled := handleStreamReadError(ev.err, cw.Disconnected(), "antigravity claude"); handled {
					return &antigravityStreamResult{usage: finishUsage(), firstTokenMs: firstTokenMs, clientDisconnect: disconnect}, nil
				}
				if errors.Is(ev.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity): max_size=%d error=%v", maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &antigravityStreamResult{usage: convertUsage(nil), firstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return nil, fmt.Errorf("stream read error: %w", ev.err)
			}

			lastDataAt = time.Now()
			s.observeAntigravityGeminiSSELine(c, ev.line)

			// 处理 SSE 行，转换为 Claude 格式
			claudeEvents := processor.ProcessLine(strings.TrimRight(ev.line, "\r\n"))
			commit.accept(processor, claudeEvents, noteWritten)

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if cw.Disconnected() {
				logger.LegacyPrintf("service.antigravity_gateway", "Upstream timeout after client disconnect (antigravity claude), returning collected usage")
				return &antigravityStreamResult{usage: finishUsage(), firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
			}
			logger.LegacyPrintf("service.antigravity_gateway", "Stream data interval timeout (antigravity)")
			sendErrorEvent("stream_timeout")
			return &antigravityStreamResult{usage: convertUsage(nil), firstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if cw.Disconnected() {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// SSE ping 事件：Anthropic 原生格式，客户端会正确处理，
			// 同时保持连接活跃防止 Cloudflare Tunnel 等代理断开
			if !cw.Fprintf("event: ping\ndata: {\"type\": \"ping\"}\n\n") {
				logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during keepalive ping (antigravity claude), continuing to drain upstream for billing")
				continue
			}
			commit.keepaliveSent = true
		}
	}
}

func (s *AntigravityGatewayService) extractImageInputSize(body []byte) string {
	var req antigravity.GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	if req.GenerationConfig != nil && req.GenerationConfig.ImageConfig != nil {
		return strings.TrimSpace(req.GenerationConfig.ImageConfig.ImageSize)
	}

	return ""
}

// isImageGenerationModel 判断模型是否为图片生成模型
// 支持的模型：gemini-3.1-flash-image, gemini-3.1-flash-lite-image, gemini-3-pro-image, gemini-2.5-flash-image 等
func isImageGenerationModel(model string) bool {
	return antigravity.IsImageModel(model)
}
