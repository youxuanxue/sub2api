package service

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
)

// tkTryForwardKiro handles the TokenKey early Kiro path before Anthropic Forward.
// When handled is true, callers must return (result, err) immediately.
func (s *GatewayService) tkTryForwardKiro(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (result *ForwardResult, handled bool, err error) {
	if account == nil || !account.IsKiro() {
		return nil, false, nil
	}
	if s.kiroGateway == nil {
		return nil, true, fmt.Errorf("kiro gateway service not configured")
	}
	// Pre-content keepalive (message_start before ping) is owned exclusively by
	// KiroGatewayService.forwardStreaming so /v1/messages and CC→Kiro share one
	// path. Do not re-bind here — a second owner would silently drift.
	result, err = s.kiroGateway.Forward(ctx, c, account, parsed, startTime)
	s.tkHandleKiroForwardUpstreamError(ctx, account, err)
	return result, true, err
}
