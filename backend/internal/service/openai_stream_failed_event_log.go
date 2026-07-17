package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func logOpenAIStreamFailedEvent(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	requestID string,
	payload []byte,
	message string,
	clientOutputStarted bool,
	passthroughMode bool,
) {
	requestModel := ""
	remoteCompact := false
	if c != nil {
		if v, ok := c.Get(OpsModelKey); ok {
			requestModel = strings.TrimSpace(anyString(v))
		}
		if c.Request != nil && c.Request.URL != nil {
			remoteCompact = strings.Contains(strings.ToLower(c.Request.URL.Path), "compact")
		}
	}
	errorCode := extractOpenAIStreamFailedErrorCode(payload)
	failoverEligible := !clientOutputStarted && openAIStreamFailedEventShouldFailover(payload, message)

	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.String("request_id", strings.TrimSpace(requestID)),
		zap.String("request_model", requestModel),
		zap.String("error_code", errorCode),
		zap.String("upstream_error_message", sanitizeUpstreamErrorMessage(message)),
		zap.Bool("client_output_started", clientOutputStarted),
		zap.Bool("failover_eligible", failoverEligible),
		zap.Bool("failover_possible", failoverEligible),
		zap.Bool("remote_compact", remoteCompact),
		zap.Bool("passthrough_mode", passthroughMode),
	}
	if account != nil {
		fields = append(fields,
			zap.Int64("account_id", account.ID),
			zap.String("account_name", account.Name),
			zap.String("platform", account.Platform),
			zap.String("account_type", account.Type),
		)
	}

	log := logger.FromContext(ctx)
	if failoverEligible {
		log.Info("openai.stream_failed_event.failover_candidate", fields...)
		return
	}
	log.Warn("openai.stream_failed_event.forwarded_to_client", fields...)
}

func extractOpenAIStreamFailedErrorCode(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range []string{
		"response.error.code",
		"error.code",
		"response.error.type",
		"error.type",
	} {
		if value := strings.TrimSpace(gjson.GetBytes(payload, path).String()); value != "" {
			return value
		}
	}
	return ""
}
