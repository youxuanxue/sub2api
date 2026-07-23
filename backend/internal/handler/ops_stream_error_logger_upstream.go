package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

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
