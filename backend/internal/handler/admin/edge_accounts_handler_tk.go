package admin

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// edgeAccountsAggregator is the narrow dependency the handler needs.
// *service.EdgeAccountsAggregator satisfies it.
type edgeAccountsAggregator interface {
	Aggregate(ctx context.Context, platform string) (*service.EdgeAccountsAggregate, error)
}

// EdgeAccountsHandler serves the prod admin "Edge Accounts" read-only overview:
// GET /api/v1/admin/edge-accounts. It fans out to every edge discovered via the
// local anthropic mirror stubs and returns each edge's account inventory.
//
// This sits behind the admin JWT auth (the /admin group) — NOT the lightweight
// edge api-key. The broad cross-fleet view is admin-only; the per-edge api-key
// (held in the mirror stub) only ever lets prod read a single edge. Credentials
// never traverse this path: the aggregator only ever decodes the edges'
// already-sanitized DTOs. TK-only; see service/edge_accounts_aggregator_tk.go.
type EdgeAccountsHandler struct {
	aggregator edgeAccountsAggregator
}

// NewEdgeAccountsHandler creates the edge accounts overview handler.
func NewEdgeAccountsHandler(aggregator edgeAccountsAggregator) *EdgeAccountsHandler {
	return &EdgeAccountsHandler{aggregator: aggregator}
}

// List GET /api/v1/admin/edge-accounts?platform=anthropic
//
// Per-edge failures are carried inside the payload (edges[].ok / .error); a 500
// is only returned when discovery itself fails (e.g. the local account list read
// or the baseline regex load).
func (h *EdgeAccountsHandler) List(c *gin.Context) {
	if h == nil || h.aggregator == nil {
		response.Error(c, 500, "edge accounts handler unavailable")
		return
	}
	platform := strings.ToLower(strings.TrimSpace(c.DefaultQuery("platform", service.PlatformAnthropic)))
	agg, err := h.aggregator.Aggregate(c.Request.Context(), platform)
	if err != nil {
		response.Error(c, 500, "failed to aggregate edge accounts")
		return
	}
	response.Success(c, agg)
}
