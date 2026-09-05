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
	// Kiro dial + reasoning can sit silent for minutes before the first
	// client-visible text/tool block. Anthropic's header-wait keepalive
	// only covers the native HTTP Do path; bind the shared pre-content
	// ping emitter here and let forwardStreaming stop it on the first
	// visible SSE write.
	hwka := s.beginHeaderWaitKeepalive(c, parsed != nil && parsed.Stream)
	bindPreContentStreamKeepalive(c, hwka)
	result, err = s.kiroGateway.Forward(ctx, c, account, parsed, startTime)
	stopPreContentStreamKeepalive(c)
	s.tkHandleKiroForwardUpstreamError(ctx, account, err)
	return result, true, err
}
