package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// schedulingCapacityReader is the narrow read-only dependency the edge capacity
// endpoint needs. service.AccountRepository satisfies it; a small interface keeps
// the handler unit-testable without stubbing the whole repository surface.
type schedulingCapacityReader interface {
	SumConcurrencyAnthropic(ctx context.Context) (int64, error)
}

// EdgeCapacityHandler serves the TokenKey "scheduling capacity" read endpoint
// that prod's anthropic-config reconciler (surface C) calls over HTTP to mirror
// each edge's live Σ schedulable concurrency onto the prod stub account.
//
// It deliberately exposes ONLY a derived capacity number — never usage, billing,
// or per-account detail — and never participates in the gateway's rate-limit /
// concurrency / billing chain (it is mounted behind a dedicated lightweight
// api-key check, see middleware/edge_capacity_auth_tk.go). This keeps the
// cross-deployment read free of scheduling side effects.
type EdgeCapacityHandler struct {
	accounts schedulingCapacityReader
}

// NewEdgeCapacityHandler wires the edge capacity handler.
func NewEdgeCapacityHandler(accounts schedulingCapacityReader) *EdgeCapacityHandler {
	return &EdgeCapacityHandler{accounts: accounts}
}

// edgeCapacityResponse is the on-the-wire shape consumed by the prod reconciler's
// surface-C step. schedulable_count is currently omitted (the Σ already encodes
// only schedulable rows); total_concurrency is the single load-bearing field.
type edgeCapacityResponse struct {
	Platform         string `json:"platform"`
	TotalConcurrency int64  `json:"total_concurrency"`
	TS               int64  `json:"ts"`
}

// GetSchedulingCapacity handles GET /api/v1/edge/scheduling-capacity.
//
// Only platform=anthropic is supported today (the only fleet surface whose prod
// stub concurrency must mirror a live edge). An unsupported / missing platform
// is rejected rather than silently defaulting, so a prod misconfig surfaces
// loudly instead of writing a wrong number.
func (h *EdgeCapacityHandler) GetSchedulingCapacity(c *gin.Context) {
	if h == nil || h.accounts == nil {
		response.Error(c, http.StatusInternalServerError, "edge capacity handler unavailable")
		return
	}

	platform := strings.ToLower(strings.TrimSpace(c.DefaultQuery("platform", "anthropic")))
	if platform != "anthropic" {
		response.Error(c, http.StatusBadRequest, "unsupported platform (only anthropic)")
		return
	}

	// Global Σ schedulable anthropic concurrency — the canonical "edge serving
	// capacity" number (same rule as the operator-Σ alignment in
	// anthropic_operator_concurrency.go). The prior by-group sum hardcoded a
	// group name ("anthropic-default") that does not exist on edges (their
	// anthropic group is "default"), so the endpoint always returned 0 and prod's
	// surface-C mirror never converged. SumConcurrencyAnthropic already filters
	// schedulable=true, so the admin-bypass api-key (schedulable=false) is excluded.
	total, err := h.accounts.SumConcurrencyAnthropic(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to read scheduling capacity")
		return
	}

	response.Success(c, edgeCapacityResponse{
		Platform:         platform,
		TotalConcurrency: total,
		TS:               time.Now().Unix(),
	})
}
