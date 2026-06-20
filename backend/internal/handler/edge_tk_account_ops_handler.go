package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// EdgeAccountOpsHandler serves the TokenKey least-privilege edge account WRITE
// ops that prod's unified /accounts page proxies to (see the prod-side forwarder
// service.EdgeAccountsAggregator.ForwardAccountOp). It is what turns the formerly
// read-only cross-edge overview into inline management: an operator clears a
// cooldown / resets quota / toggles scheduling / queries usage on an edge-local
// account WITHOUT leaving prod and WITHOUT the full admin-session handoff.
//
// SCOPE — a deliberate WHITELIST of status-class mutations that NEVER touch
// credentials: clear-rate-limit, reset-quota, clear-temp-unschedulable,
// schedulable toggle, and an active usage query. Credential-class ops (create /
// edit / delete / OAuth reauth) are intentionally ABSENT: they stay on the edge's
// own /admin/accounts via the admin-session handoff, so secrets never traverse
// prod. Each mutation re-reads the account and returns the SAME credential-free
// edgeAccountDTO the read endpoint (edge_tk_accounts_handler.go) emits, so prod's
// panel merges the post-op state without exposing anything new.
//
// AUTH — mounted behind NewEdgeCapacityAuthMiddleware (active key) PLUS
// NewEdgeAdminOwnerMiddleware (key owner is an active admin): a plain relay key
// can read the inventory but only an admin-owned key may mutate.
//
// :id is the edge-LOCAL account id (the edge's own DB primary key), distinct from
// the prod stub id — prod addresses it as the composite edge:<edge_id>:<local_id>
// and the forwarder routes by edge_id, so this handler only ever sees a local id.
type EdgeAccountOpsHandler struct {
	rateLimit edgeOpsRateLimiter
	admin     edgeOpsAdmin
	usage     edgeOpsUsage
}

// edgeOpsRateLimiter clears cooldown state. *service.RateLimitService satisfies it.
type edgeOpsRateLimiter interface {
	ClearRateLimit(ctx context.Context, id int64) error
	ClearTempUnschedulable(ctx context.Context, id int64) error
}

// edgeOpsAdmin performs the quota/schedulable mutations and re-reads the account.
// service.AdminService satisfies it.
type edgeOpsAdmin interface {
	ResetAccountQuota(ctx context.Context, id int64) error
	SetAccountSchedulable(ctx context.Context, id int64, schedulable bool) (*service.Account, error)
	GetAccount(ctx context.Context, id int64) (*service.Account, error)
}

// edgeOpsUsage runs the active/passive usage query. *service.AccountUsageService
// satisfies it (same methods the admin GetUsage handler uses).
type edgeOpsUsage interface {
	GetUsage(ctx context.Context, accountID int64, force ...bool) (*service.UsageInfo, error)
	GetPassiveUsage(ctx context.Context, accountID int64) (*service.UsageInfo, error)
}

// NewEdgeAccountOpsHandler wires the edge account ops handler. Any dependency may
// be nil; a handler whose dependency is nil returns 500 (handler unavailable)
// rather than panicking, mirroring the other edge handlers' nil-safety.
func NewEdgeAccountOpsHandler(rateLimit edgeOpsRateLimiter, admin edgeOpsAdmin, usage edgeOpsUsage) *EdgeAccountOpsHandler {
	return &EdgeAccountOpsHandler{rateLimit: rateLimit, admin: admin, usage: usage}
}

// parseAccountID parses :id; responds 400 and returns ok=false on a bad id.
func (h *EdgeAccountOpsHandler) parseAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.Error(c, http.StatusBadRequest, "invalid account id")
		return 0, false
	}
	return id, true
}

// respondWithAccount re-reads the (just-mutated) account and emits the sanitized,
// credential-free DTO so the prod panel can merge the post-op state.
func (h *EdgeAccountOpsHandler) respondWithAccount(c *gin.Context, id int64) {
	acc, err := h.admin.GetAccount(c.Request.Context(), id)
	if err != nil || acc == nil {
		response.Error(c, http.StatusInternalServerError, "failed to load account after op")
		return
	}
	response.Success(c, toEdgeAccountDTO(acc))
}

// ClearRateLimit handles POST /api/v1/edge/accounts/:id/clear-rate-limit.
func (h *EdgeAccountOpsHandler) ClearRateLimit(c *gin.Context) {
	if h == nil || h.rateLimit == nil || h.admin == nil {
		response.Error(c, http.StatusInternalServerError, "edge account ops handler unavailable")
		return
	}
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	if err := h.rateLimit.ClearRateLimit(c.Request.Context(), id); err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to clear rate limit")
		return
	}
	h.respondWithAccount(c, id)
}

// ResetQuota handles POST /api/v1/edge/accounts/:id/reset-quota.
func (h *EdgeAccountOpsHandler) ResetQuota(c *gin.Context) {
	if h == nil || h.admin == nil {
		response.Error(c, http.StatusInternalServerError, "edge account ops handler unavailable")
		return
	}
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	if err := h.admin.ResetAccountQuota(c.Request.Context(), id); err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to reset quota")
		return
	}
	h.respondWithAccount(c, id)
}

// ClearTempUnschedulable handles DELETE /api/v1/edge/accounts/:id/temp-unschedulable.
func (h *EdgeAccountOpsHandler) ClearTempUnschedulable(c *gin.Context) {
	if h == nil || h.rateLimit == nil || h.admin == nil {
		response.Error(c, http.StatusInternalServerError, "edge account ops handler unavailable")
		return
	}
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	if err := h.rateLimit.ClearTempUnschedulable(c.Request.Context(), id); err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to clear temp unschedulable")
		return
	}
	h.respondWithAccount(c, id)
}

// edgeSetSchedulableRequest is the schedulable-toggle body, mirroring the admin
// handler's SetSchedulableRequest.
type edgeSetSchedulableRequest struct {
	Schedulable bool `json:"schedulable"`
}

// SetSchedulable handles POST /api/v1/edge/accounts/:id/schedulable.
func (h *EdgeAccountOpsHandler) SetSchedulable(c *gin.Context) {
	if h == nil || h.admin == nil {
		response.Error(c, http.StatusInternalServerError, "edge account ops handler unavailable")
		return
	}
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	var req edgeSetSchedulableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	acc, err := h.admin.SetAccountSchedulable(c.Request.Context(), id, req.Schedulable)
	if err != nil || acc == nil {
		response.Error(c, http.StatusInternalServerError, "failed to set schedulable")
		return
	}
	// SetAccountSchedulable returns the updated account; emit it directly.
	response.Success(c, toEdgeAccountDTO(acc))
}

// GetActiveUsage handles GET /api/v1/edge/accounts/:id/usage?source=active|passive&force=.
// Default source=active runs a real upstream query on THIS edge (the "查询"
// button); source=passive returns the persisted-sample windows without an
// upstream call. Mirrors the admin GetUsage handler so the shared usage cell
// behaves identically for an edge account.
func (h *EdgeAccountOpsHandler) GetActiveUsage(c *gin.Context) {
	if h == nil || h.usage == nil {
		response.Error(c, http.StatusInternalServerError, "edge account ops handler unavailable")
		return
	}
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	source := c.DefaultQuery("source", "active")
	force := c.Query("force") == "true"

	var (
		usage *service.UsageInfo
		err   error
	)
	if source == "passive" {
		usage, err = h.usage.GetPassiveUsage(c.Request.Context(), id)
	} else {
		usage, err = h.usage.GetUsage(c.Request.Context(), id, force)
	}
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to query usage")
		return
	}
	response.Success(c, usage)
}
