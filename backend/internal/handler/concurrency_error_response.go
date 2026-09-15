package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// statusClientClosedRequest aliases the service-layer owner
// (client-closed-499-ssot) for the handler package's terse call sites. The
// literal value lives only in service/client_closed_request_tk.go.
const statusClientClosedRequest = service.StatusClientClosedRequest

const (
	gatewayQueueFullCode        = "gateway_queue_full"
	gatewayConcurrencyLimitCode = "gateway_concurrency_limit"
)

func concurrencyErrorResponse(err error, slotType string) (int, string, string, string) {
	var waitQueueFullErr *WaitQueueFullError
	if errors.As(err, &waitQueueFullErr) {
		return http.StatusTooManyRequests, "rate_limit_error", gatewayQueueFullCode,
			"Too many pending requests, please retry later"
	}

	var concurrencyErr *ConcurrencyError
	if errors.As(err, &concurrencyErr) {
		if concurrencyErr.SlotType != "" {
			slotType = concurrencyErr.SlotType
		}
		return http.StatusTooManyRequests, "rate_limit_error", gatewayConcurrencyLimitCode,
			fmt.Sprintf("Concurrency limit exceeded for %s, please retry later", slotType)
	}

	// Bare context.Canceled keeps its language-level check: the acquire path
	// only unwraps real cancellations, and a server-side acquire deadline must
	// stay 503 (service.IsClientClosedRequest enforces the same precedence).
	if errors.Is(err, context.Canceled) {
		return statusClientClosedRequest, "api_error", "", "context canceled"
	}

	return http.StatusServiceUnavailable, "api_error", "", "Service temporarily unavailable, please retry later"
}
