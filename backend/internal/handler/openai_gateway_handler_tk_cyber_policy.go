package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// writeCyberSessionBlockedWSError sends an error frame telling the client this
// session is blocked by the cyber session block (F5a) before closing.
func writeCyberSessionBlockedWSError(ctx context.Context, conn *coderws.Conn) {
	if conn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(gin.H{
		"event_id": "evt_cyber_session_blocked",
		"type":     "error",
		"error": gin.H{
			"type":    "permission_error",
			"code":    "session_blocked_by_cyber_policy",
			"message": cyberSessionBlockedClientMsg,
		},
	})
	if err != nil {
		payload = []byte(`{"event_id":"evt_cyber_session_blocked","type":"error","error":{"type":"permission_error","code":"session_blocked_by_cyber_policy","message":"This session is blocked by cyber-security policy, please start a new session"}}`)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
}

// cyberPolicyRecordedKey guards against double-firing recordCyberPolicyIfMarked
// within one request (e.g. in a retry/failover loop).
const cyberPolicyRecordedKey = "ops_cyber_recorded"

// cyberPolicyOpsErrorMeta carries request-scoped fields captured outside the
// async goroutine for building the cyber ops_error_logs entry.
type cyberPolicyOpsErrorMeta struct {
	RequestID       string
	ClientRequestID string
	Platform        string
	Model           string
	RequestPath     string
	Stream          bool
	InboundEndpoint string
	UserAgent       string
	APIKeyPrefix    string
	UserID          int64
	APIKeyID        int64
	AccountID       int64
	GroupID         *int64
	ClientIP        string
	CreatedAt       time.Time
	SessionBlockKey string
}

// buildCyberPolicyOpsErrorEntry builds the ops_error_logs entry for an upstream
// cyber_policy hit. StatusCode mirrors what the codex client actually received
// (400 non-stream / 200 stream), per F6.
func buildCyberPolicyOpsErrorEntry(meta cyberPolicyOpsErrorMeta, mark *service.CyberPolicyMark) *service.OpsInsertErrorLogInput {
	rt := int16(service.RequestTypeCyberBlocked)
	entry := &service.OpsInsertErrorLogInput{
		RequestID:         meta.RequestID,
		ClientRequestID:   meta.ClientRequestID,
		Platform:          meta.Platform,
		Model:             meta.Model,
		RequestPath:       meta.RequestPath,
		Stream:            meta.Stream,
		InboundEndpoint:   meta.InboundEndpoint,
		RequestType:       &rt,
		UserAgent:         meta.UserAgent,
		APIKeyPrefix:      meta.APIKeyPrefix,
		ErrorPhase:        "request",
		ErrorType:         "cyber_policy",
		Severity:          "P3",
		StatusCode:        mark.UpstreamStatus,
		IsBusinessLimited: true,
		ErrorMessage:      "cyber_policy: " + mark.Message,
		// 原始 body 直接入队；ops service 落库前统一走 sanitizeErrorBodyForStorage 脱敏与截断。
		ErrorBody:   mark.Body,
		ErrorSource: "upstream_http",
		ErrorOwner:  "provider",
		CreatedAt:   meta.CreatedAt,
	}
	if meta.UserID > 0 {
		entry.UserID = &meta.UserID
	}
	if meta.APIKeyID > 0 {
		entry.APIKeyID = &meta.APIKeyID
	}
	if meta.AccountID > 0 {
		entry.AccountID = &meta.AccountID
	}
	entry.GroupID = meta.GroupID
	if meta.ClientIP != "" {
		entry.ClientIP = &meta.ClientIP
	}
	return entry
}

// 双语单串：网关客户端面向中英用户，且本错误无 i18n 协商通道。
const cyberSessionBlockedClientMsg = "该会话已被网络安全策略屏蔽，请开启新会话 / This session is blocked by cyber-security policy, please start a new session"

// buildCyberSessionBlockedOpsEntry builds the ops_error_logs entry for a request
// rejected locally by the cyber session block (F5a). Distinct error_type from
// upstream `cyber_policy`; never feeds moderation logs / violation counting
// (the request never reached upstream — see spec).
func buildCyberSessionBlockedOpsEntry(meta cyberPolicyOpsErrorMeta) *service.OpsInsertErrorLogInput {
	rt := int16(service.RequestTypeCyberBlocked)
	entry := &service.OpsInsertErrorLogInput{
		RequestID:         meta.RequestID,
		ClientRequestID:   meta.ClientRequestID,
		Platform:          meta.Platform,
		Model:             meta.Model,
		RequestPath:       meta.RequestPath,
		Stream:            meta.Stream,
		InboundEndpoint:   meta.InboundEndpoint,
		RequestType:       &rt,
		UserAgent:         meta.UserAgent,
		APIKeyPrefix:      meta.APIKeyPrefix,
		ErrorPhase:        "request",
		ErrorType:         "cyber_policy_session_blocked",
		Severity:          "P3",
		StatusCode:        http.StatusForbidden,
		IsBusinessLimited: true,
		ErrorMessage:      "cyber_policy_session_blocked: request rejected locally by session block",
		ErrorSource:       "gateway_local",
		ErrorOwner:        "platform",
		CreatedAt:         meta.CreatedAt,
		// AccountID 有意不设：请求在账号选择前即被拒绝。
	}
	if meta.SessionBlockKey != "" {
		entry.ErrorBody = "session_block_key=" + meta.SessionBlockKey
	}
	if meta.UserID > 0 {
		entry.UserID = &meta.UserID
	}
	if meta.APIKeyID > 0 {
		entry.APIKeyID = &meta.APIKeyID
	}
	entry.GroupID = meta.GroupID
	if meta.ClientIP != "" {
		entry.ClientIP = &meta.ClientIP
	}
	return entry
}

// cyberSessionBlockFormat selects the per-endpoint error envelope for a locally
// blocked session (用户决策：兼容路径各自格式).
type cyberSessionBlockFormat int

const (
	cyberBlockFormatResponses cyberSessionBlockFormat = iota
	cyberBlockFormatChat
	cyberBlockFormatAnthropic
)

// rejectIfCyberSessionBlocked checks the session-block table BEFORE account
// selection. Returns true when the request was rejected (response already
// written + ops entry enqueued). Fail-open: disabled switch / empty key /
// store error → false.
func (h *OpenAIGatewayHandler) rejectIfCyberSessionBlocked(c *gin.Context, apiKey *service.APIKey, body []byte, model string, format cyberSessionBlockFormat) bool {
	if h == nil || h.gatewayService == nil || apiKey == nil {
		return false
	}
	// 开关默认关：先走 ~ns 级缓存开关检查，再付出 key 派生(gjson+sha256)成本。
	if enabled, _ := h.gatewayService.CyberSessionBlockRuntime(c.Request.Context()); !enabled {
		return false
	}
	key := findBlockedCyberSessionKey(c.Request.Context(), h.gatewayService, apiKey.ID, c, body)
	if key == "" {
		return false
	}
	// body-signal compact 心跳可能已把响应头提交为 200（cyber 检查在用户槽位
	// 长等待之后执行）：以 response.failed 终止事件回传；未提交时停拍后照常
	// 写 JSON（#3887）。
	if service.StopOpenAICompactSSEKeepaliveCommitted(c) {
		service.MarkOpsStreamError(c, "permission_error", cyberSessionBlockedClientMsg, http.StatusForbidden)
		if writeResponsesFailedSSE(c, "permission_error", cyberSessionBlockedClientMsg) {
			h.enqueueCyberSessionBlockedOpsEntry(c, apiKey, model, key)
			return true
		}
	}
	switch format {
	case cyberBlockFormatAnthropic:
		c.JSON(http.StatusForbidden, gin.H{"type": "error", "error": gin.H{
			"type":    "permission_error",
			"message": cyberSessionBlockedClientMsg,
		}})
	default: // cyberBlockFormatResponses 与 cyberBlockFormatChat：同构的 OpenAI error envelope
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"type":    "permission_error",
			"code":    "session_blocked_by_cyber_policy",
			"message": cyberSessionBlockedClientMsg,
		}})
	}
	h.enqueueCyberSessionBlockedOpsEntry(c, apiKey, model, key)
	return true
}

type cyberSessionBlockWritePlan struct {
	scopeKey string
	keys     []string
}

func buildCyberSessionBlockWritePlan(apiKeyID int64, c *gin.Context, body []byte) cyberSessionBlockWritePlan {
	plan := cyberSessionBlockWritePlan{}
	if key := service.CyberSessionExplicitBlockKey(apiKeyID, c, body); key != "" {
		plan.keys = append(plan.keys, key)
	}
	transcriptKeys := service.CyberSessionTranscriptBlockKeys(apiKeyID, body)
	for _, key := range transcriptKeys {
		if len(plan.keys) == 0 || key != plan.keys[0] {
			plan.keys = append(plan.keys, key)
		}
	}
	if len(transcriptKeys) > 0 {
		plan.scopeKey = cyberSessionScopeKey(apiKeyID, c)
	}
	return plan
}

func findBlockedCyberSessionKey(ctx context.Context, gatewayService *service.OpenAIGatewayService, apiKeyID int64, c *gin.Context, body []byte) string {
	if gatewayService == nil {
		return ""
	}
	clientIP, userAgent := "", ""
	if c != nil {
		clientIP = strings.TrimSpace(ip.GetClientIP(c))
		userAgent = c.GetHeader("User-Agent")
	}
	return gatewayService.FindCyberSessionBlockedForRequest(ctx, apiKeyID, c, body, clientIP, userAgent)
}

func cyberSessionScopeKey(apiKeyID int64, c *gin.Context) string {
	if c == nil {
		return ""
	}
	return service.CyberSessionScopeKey(apiKeyID, strings.TrimSpace(ip.GetClientIP(c)), c.GetHeader("User-Agent"))
}

// enqueueCyberSessionBlockedOpsEntry captures request meta and enqueues the
// ops_error_logs entry for a locally blocked request.
func (h *OpenAIGatewayHandler) enqueueCyberSessionBlockedOpsEntry(c *gin.Context, apiKey *service.APIKey, model string, sessionBlockKey string) {
	if h.opsService == nil {
		return
	}
	// The dedicated cyber_session_blocked entry owns Ops semantics for this
	// request; suppress the generic middleware record of the same 403 response.
	c.Set(opsDedicatedErrorRecordedKey, true)
	meta := cyberPolicyOpsErrorMeta{Model: model, InboundEndpoint: GetInboundEndpoint(c), CreatedAt: time.Now(), SessionBlockKey: sessionBlockKey}
	meta.RequestID = c.Writer.Header().Get("X-Request-Id")
	if c.Request != nil && c.Request.URL != nil {
		meta.RequestPath = c.Request.URL.Path
	}
	if v, ok := c.Get(opsStreamKey); ok {
		if b, ok := v.(bool); ok {
			meta.Stream = b
		}
	}
	requestCtx := context.Background()
	if c.Request != nil {
		requestCtx = c.Request.Context()
	}
	meta.Platform = resolveOpsPlatform(requestCtx, apiKey, guessPlatformFromPath(meta.RequestPath))
	if c.Request != nil {
		meta.ClientRequestID, _ = c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		meta.UserAgent = c.GetHeader("User-Agent")
		meta.ClientIP = strings.TrimSpace(ip.GetClientIP(c))
	}
	meta.APIKeyID = apiKey.ID
	meta.GroupID = apiKey.GroupID
	meta.APIKeyPrefix = keyPrefix(apiKey.Key, 8)
	if apiKey.User != nil {
		meta.UserID = apiKey.User.ID
	}
	enqueueOpsErrorLog(h.opsService, buildCyberSessionBlockedOpsEntry(meta))
}

// recordCyberPolicyIfMarked 在 gateway forward 返回后检查 cyber 标记，异步写风控日志/邮件，
// 并在 forward 返回错误时写一条 tokens=0 用量行。标记由 gateway 服务层在透传 cyber 后设置；
// 当前请求已发给用户，本方法只做事后记录，不影响响应。forwardErrored 为 true 时才写用量行，
// 避免与正常 RecordUsage(forward 成功路径)重复。每请求至多记录一次。
func (h *OpenAIGatewayHandler) recordCyberPolicyIfMarked(c *gin.Context, apiKey *service.APIKey, account *service.Account, subscription *service.UserSubscription, model string, forwardErrored bool, cyberBlockBody []byte, channelFields service.ChannelUsageFields, requestPayloadHash string) {
	mark := service.GetOpsCyberPolicy(c)
	if mark == nil {
		return
	}
	if c.GetBool(cyberPolicyRecordedKey) {
		return
	}
	c.Set(cyberPolicyRecordedKey, true)
	model = clientRequestedModel(c, model)
	requestCtx := context.Background()
	if c.Request != nil {
		requestCtx = c.Request.Context()
	}
	apiKey, subscription = snapshotCandidateBilling(requestCtx, apiKey, subscription)

	requestID := c.Writer.Header().Get("X-Request-Id")
	var userID, apiKeyID int64
	var userEmail, apiKeyName, groupName string
	var groupID *int64
	if apiKey != nil {
		apiKeyID = apiKey.ID
		apiKeyName = apiKey.Name
		groupID = apiKey.GroupID
		if apiKey.User != nil {
			userID = apiKey.User.ID
			userEmail = apiKey.User.Email
		}
		if apiKey.Group != nil {
			groupName = apiKey.Group.Name
		}
	}
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := ""
	var accountID int64
	if account != nil {
		accountID = account.ID
		upstreamEndpoint = resolveOpenAIUpstreamEndpoint(c, account, nil)
	}
	stream := false
	if v, ok := c.Get(opsStreamKey); ok {
		if b, ok := v.(bool); ok {
			stream = b
		}
	}
	cmSvc := h.contentModerationService
	gwSvc := h.gatewayService
	opsSvc := h.opsService
	apiKeySvc := h.apiKeyService
	requestPath := ""
	if c.Request != nil && c.Request.URL != nil {
		requestPath = c.Request.URL.Path
	}
	platform := resolveOpsPlatform(requestCtx, apiKey, guessPlatformFromPath(requestPath))
	var clientRequestID, userAgent, clientIPStr string
	if c.Request != nil {
		clientRequestID, _ = c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		userAgent = c.GetHeader("User-Agent")
		clientIPStr = strings.TrimSpace(ip.GetClientIP(c))
	}
	// 提前拍成标量，避免在下方 goroutine 内访问 gin.Context。
	sessionID := service.ExtractClientSessionID(c)
	apiKeyPrefix := ""
	if apiKey != nil {
		apiKeyPrefix = keyPrefix(apiKey.Key, 8)
	}
	opsMeta := cyberPolicyOpsErrorMeta{
		RequestID:       requestID,
		ClientRequestID: clientRequestID,
		Platform:        platform,
		Model:           model,
		RequestPath:     requestPath,
		Stream:          stream,
		InboundEndpoint: inboundEndpoint,
		UserAgent:       userAgent,
		APIKeyPrefix:    apiKeyPrefix,
		UserID:          userID,
		APIKeyID:        apiKeyID,
		AccountID:       accountID,
		GroupID:         groupID,
		ClientIP:        clientIPStr,
		CreatedAt:       time.Now(),
	}
	if gwSvc != nil && apiKey != nil {
		plan := buildCyberSessionBlockWritePlan(apiKey.ID, c, cyberBlockBody)
		if len(plan.keys) > 0 {
			blockCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			gwSvc.MarkCyberSessionBlocked(blockCtx, plan.scopeKey, plan.keys)
			cancel()
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cmSvc != nil {
			cmSvc.RecordCyberPolicyEvent(ctx, service.CyberPolicyRecordInput{
				RequestID:       requestID,
				UserID:          userID,
				UserEmail:       userEmail,
				APIKeyID:        apiKeyID,
				APIKeyName:      apiKeyName,
				GroupID:         groupID,
				GroupName:       groupName,
				Endpoint:        inboundEndpoint,
				Model:           model,
				UpstreamMessage: mark.Message,
				UpstreamBody:    mark.Body,
				UpstreamStatus:  mark.UpstreamStatus,
				UpstreamInTok:   mark.UpstreamInTok,
				UpstreamOutTok:  mark.UpstreamOutTok,
			})
		}
		if forwardErrored && gwSvc != nil {
			gwSvc.RecordCyberPolicyUsageLog(ctx, service.CyberPolicyUsageInput{
				APIKey:             apiKey,
				Account:            account,
				Subscription:       subscription,
				RequestID:          requestID,
				Model:              model,
				Stream:             stream,
				InputTokens:        mark.UpstreamInTok,
				OutputTokens:       mark.UpstreamOutTok,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIPStr,
				SessionID:          sessionID,
				RequestPayloadHash: requestPayloadHash,
				APIKeyService:      apiKeySvc,
				ChannelUsageFields: channelFields,
			})
		}
		if opsSvc != nil {
			enqueueOpsErrorLog(opsSvc, buildCyberPolicyOpsErrorEntry(opsMeta, mark))
		}
	}()
}

// clearCyberPolicyTurnState resets the cyber mark and the per-request recorded
// guard. WS-only: called at the END of AfterTurn, after recordCyberPolicyIfMarked
// and RecordUsage (which reads CyberBlocked) have both consumed the mark.
func clearCyberPolicyTurnState(c *gin.Context) {
	if c == nil {
		return
	}
	service.ClearOpsCyberPolicy(c)
	c.Set(cyberPolicyRecordedKey, false)
}
