package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func finalizeOpsFirstOutputEvaluation(c *gin.Context, writer *opsCaptureWriter) {
	value, _ := c.Get(service.OpsUpstreamErrorsKey)
	events, _ := value.([]*service.OpsUpstreamErrorEvent)
	hasTimeout := false
	for _, event := range events {
		if event != nil && event.Kind == "first_output_timeout" {
			hasTimeout = true
			break
		}
	}
	if !hasTimeout {
		return
	}
	status := c.Writer.Status()
	parsed := parseOpsErrorResponse(writer.capturedBytes())
	if !parsed.StreamFailure {
		if terminal, ok := writer.capturedTerminalError(); ok {
			parsed = terminal
		}
	}
	if status < 400 && parsed.StreamFailure {
		status = inferStreamFailureStatus(c, parsed)
	}
	outcome := "recovered"
	if status >= 400 || len(service.GetOpsStreamErrors(c)) > 0 || c.GetBool("ops_first_output_ws_failed") {
		outcome = "final_failure"
	}
	if status == service.StatusClientClosedRequest || errors.Is(c.Request.Context().Err(), context.Canceled) {
		outcome = "client_cancel"
	}
	if !service.FinalizeOpsFirstOutputEvaluation(c, outcome) {
		return
	}
	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	logger.L().Info("gateway.first_output_timeout_outcome",
		zap.String("component", "audit.first_output"),
		zap.String("request_id", requestID), zap.String("timeout_outcome", outcome),
		zap.Int("final_status", status), zap.Int("attempt_count", service.OpsFirstOutputAttemptCount(c)))
}
