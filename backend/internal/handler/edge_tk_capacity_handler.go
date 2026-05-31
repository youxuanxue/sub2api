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
	SumConcurrencyAnthropicByGroup(ctx context.Context, groupName string) (int64, error)
}

// anthropicDefaultGroupName is the canonical default group for anthropic accounts
// (group naming convention is "<platform>-default"). Surface-C counts only this
// group's schedulable concurrency so prod mirrors the right edge pool number.
const anthropicDefaultGroupName = "anthropic-default"

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

	total, err := h.accounts.SumConcurrencyAnthropicByGroup(c.Request.Context(), anthropicDefaultGroupName)
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
