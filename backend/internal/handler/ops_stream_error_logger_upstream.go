package handler

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func logOpsStreamError(c *gin.Context, ops *service.OpsService, wireStatus int) {
	streamErr, ok := service.GetOpsStreamError(c)
	if !ok {
		return
	}
	if v, ok := c.Get(service.OpsSkipPassthroughKey); ok {
		if skip, _ := v.(bool); skip {
			return
		}
	}
	if shouldSkipOpsErrorLog(c.Request.Context(), ops, streamErr.Message, streamErr.Message, c.Request.URL.Path) {
		return
	}

	classifyStatus := streamErr.IntendedStatus
	if classifyStatus <= 0 {
		classifyStatus = wireStatus
	}
	normalizedType := normalizeOpsErrorType(streamErr.ErrType, "")
	phase, errorOwner, errorSource := classifyOpsErrorLog(c, normalizedType, streamErr.Message, "", classifyStatus)
	apiKey := getOpsAPIKey(c)
	clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	model, _ := c.Get(opsModelKey)
	modelName, _ := model.(string)
	accountIDV, _ := c.Get(opsAccountIDKey)
	var accountID *int64
	if value, ok := accountIDV.(int64); ok && value > 0 {
		accountID = &value
	}
	platform := resolveOpsPlatform(apiKey, guessPlatformFromPath(c.Request.URL.Path))
	requestID := c.Writer.Header().Get("X-Request-Id")
	if requestID == "" {
		requestID = c.Writer.Header().Get("x-request-id")
	}

	entry := &service.OpsInsertErrorLogInput{
		RequestID:        requestID,
		ClientRequestID:  clientRequestID,
		AccountID:        accountID,
		Platform:         platform,
		Model:            modelName,
		RequestPath:      c.Request.URL.Path,
		Stream:           true,
		InboundEndpoint:  GetInboundEndpoint(c),
		UpstreamEndpoint: GetUpstreamEndpoint(c, platform),
		RequestedModel:   modelName,
		UpstreamModel: func() string {
			value, _ := c.Get(opsUpstreamModelKey)
			mapped, _ := value.(string)
			return strings.TrimSpace(mapped)
		}(),
		RequestType: func() *int16 {
			value, _ := c.Get(opsRequestTypeKey)
			switch typed := value.(type) {
			case int16:
				return &typed
			case int:
				converted := int16(typed)
				return &converted
			default:
				return nil
			}
		}(),
		UserAgent:     c.GetHeader("User-Agent"),
		ErrorPhase:    phase,
		ErrorType:     normalizedType,
		Severity:      classifyOpsSeverity(normalizedType, classifyStatus),
		StatusCode:    wireStatus,
		IsCountTokens: isCountTokensRequest(c),
		ErrorMessage:  streamErr.Message,
		ErrorSource:   errorSource,
		ErrorOwner:    errorOwner,
		CreatedAt:     time.Now(),
	}
	applyOpsLatencyFieldsFromContext(c, entry)
	applyOpsUpstreamFieldsFromContext(c, entry)
	if apiKey != nil {
		entry.APIKeyID = &apiKey.ID
		entry.APIKeyPrefix = keyPrefix(apiKey.Key, 8)
		if apiKey.User != nil {
			entry.UserID = &apiKey.User.ID
		}
		if apiKey.GroupID != nil {
			entry.GroupID = apiKey.GroupID
		}
		if apiKey.Group != nil && apiKey.Group.Platform != "" {
			entry.Platform = apiKey.Group.Platform
		}
	}
	if clientIP := strings.TrimSpace(ip.GetClientIP(c)); clientIP != "" {
		entry.ClientIP = &clientIP
	}
	enqueueOpsErrorLog(ops, entry)
}

func applyOpsUpstreamFieldsFromContext(c *gin.Context, entry *service.OpsInsertErrorLogInput) {
	if c == nil || entry == nil {
		return
	}
	if value, ok := c.Get(service.OpsUpstreamStatusCodeKey); ok {
		switch typed := value.(type) {
		case int:
			if typed > 0 {
				code := typed
				entry.UpstreamStatusCode = &code
			}
		case int64:
			if typed > 0 {
				code := int(typed)
				entry.UpstreamStatusCode = &code
			}
		}
	}
	if value, ok := c.Get(service.OpsUpstreamErrorMessageKey); ok {
		if message, ok := value.(string); ok && strings.TrimSpace(message) != "" {
			message = strings.TrimSpace(message)
			entry.UpstreamErrorMessage = &message
		}
	}
	if value, ok := c.Get(service.OpsUpstreamErrorDetailKey); ok {
		if detail, ok := value.(string); ok && strings.TrimSpace(detail) != "" {
			detail = strings.TrimSpace(detail)
			entry.UpstreamErrorDetail = &detail
		}
	}
	value, ok := c.Get(service.OpsUpstreamErrorsKey)
	if !ok {
		return
	}
	events, ok := value.([]*service.OpsUpstreamErrorEvent)
	if !ok || len(events) == 0 {
		return
	}
	entry.UpstreamErrors = events
	last := events[len(events)-1]
	if last == nil {
		return
	}
	if last.Stage == string(service.GatewayFailureStageAccountAuth) {
		code := 0
		entry.UpstreamStatusCode = &code
		entry.UpstreamErrorMessage = nil
		if message := strings.TrimSpace(last.Message); message != "" {
			entry.UpstreamErrorMessage = &message
		}
		entry.UpstreamErrorDetail = nil
		if detail := strings.TrimSpace(last.Detail); detail != "" {
			entry.UpstreamErrorDetail = &detail
		}
		return
	}
	if entry.UpstreamStatusCode == nil && last.UpstreamStatusCode > 0 {
		code := last.UpstreamStatusCode
		entry.UpstreamStatusCode = &code
	}
}
