package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// edgeAccountOpForwarder is the narrow dependency the prod proxy needs.
// *service.EdgeAccountsAggregator satisfies it.
type edgeAccountOpForwarder interface {
	ForwardAccountOp(ctx context.Context, edgeID string, localID int64, op service.EdgeAccountOp, rawQuery string, body []byte) (int, []byte, error)
}

// EdgeAccountOpsHandler is the prod-side thin proxy that lets the unified
// /accounts page manage an EDGE-local account inline. It maps a prod admin route
//
//	POST|DELETE|GET /api/v1/admin/edge-accounts/:edge/accounts/:id/<op>
//
// onto the target edge's least-privilege ops endpoint via the aggregator's
// ForwardAccountOp (mirror-stub x-api-key, per-edge isolation). It is admin-JWT
// gated (inherited from the /admin group) — only a prod admin can drive a
// cross-edge write — and it relays the edge's credential-free response verbatim,
// adding nothing of its own.
//
// SCOPE mirrors the edge handler: a WHITELIST of status-class ops (clear-rate-limit
// / reset-quota / temp-unschedulable / schedulable / active usage query) that never
// touch credentials. Credential-class ops (create/edit/delete/reauth) are NOT here;
// they remain on the edge via the admin-session handoff (see MintAdminSession).
type EdgeAccountOpsHandler struct {
	forwarder edgeAccountOpForwarder
}

// NewEdgeAccountOpsHandler creates the prod-side edge account ops proxy handler.
func NewEdgeAccountOpsHandler(forwarder edgeAccountOpForwarder) *EdgeAccountOpsHandler {
	return &EdgeAccountOpsHandler{forwarder: forwarder}
}

// ClearRateLimit POST /api/v1/admin/edge-accounts/:edge/accounts/:id/clear-rate-limit
func (h *EdgeAccountOpsHandler) ClearRateLimit(c *gin.Context) {
	h.forward(c, service.EdgeAccountOpClearRateLimit, false, "")
}

// ResetQuota POST /api/v1/admin/edge-accounts/:edge/accounts/:id/reset-quota
func (h *EdgeAccountOpsHandler) ResetQuota(c *gin.Context) {
	h.forward(c, service.EdgeAccountOpResetQuota, false, "")
}

// ClearTempUnschedulable DELETE /api/v1/admin/edge-accounts/:edge/accounts/:id/temp-unschedulable
func (h *EdgeAccountOpsHandler) ClearTempUnschedulable(c *gin.Context) {
	h.forward(c, service.EdgeAccountOpClearTempUnsched, false, "")
}

// SetSchedulable POST /api/v1/admin/edge-accounts/:edge/accounts/:id/schedulable
// Forwards the JSON body ({"schedulable": bool}) to the edge.
func (h *EdgeAccountOpsHandler) SetSchedulable(c *gin.Context) {
	h.forward(c, service.EdgeAccountOpSetSchedulable, true, "")
}

// GetActiveUsage GET /api/v1/admin/edge-accounts/:edge/accounts/:id/usage?source=&force=
// Only the source + force params are forwarded (rebuilt clean), never arbitrary query.
func (h *EdgeAccountOpsHandler) GetActiveUsage(c *gin.Context) {
	q := url.Values{}
	if source := strings.TrimSpace(c.Query("source")); source != "" {
		q.Set("source", source)
	}
	if c.Query("force") == "true" {
		q.Set("force", "true")
	}
	h.forward(c, service.EdgeAccountOpUsage, false, q.Encode())
}

// forward resolves :edge + :id, optionally reads the JSON body, calls the
// forwarder, and relays the edge's status + body verbatim. Unreachable edge → 502;
// unknown edge → 404; otherwise the edge's own status/body pass through so the UI
// sees the real result (the updated credential-free DTO / usage / error envelope).
func (h *EdgeAccountOpsHandler) forward(c *gin.Context, op service.EdgeAccountOp, withBody bool, rawQuery string) {
	if h == nil || h.forwarder == nil {
		response.Error(c, http.StatusInternalServerError, "edge account ops handler unavailable")
		return
	}

	edgeID := strings.ToLower(strings.TrimSpace(c.Param("edge")))
	if edgeID == "" {
		response.Error(c, http.StatusBadRequest, "edge id required")
		return
	}
	localID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || localID <= 0 {
		response.Error(c, http.StatusBadRequest, "invalid account id")
		return
	}

	var body []byte
	if withBody {
		// Cap the relayed body — these are tiny toggle payloads.
		b, readErr := io.ReadAll(io.LimitReader(c.Request.Body, 1<<16))
		if readErr != nil {
			response.Error(c, http.StatusBadRequest, "failed to read request body")
			return
		}
		body = b
	}

	status, respBody, err := h.forwarder.ForwardAccountOp(c.Request.Context(), edgeID, localID, op, rawQuery, body)
	if err != nil {
		if errors.Is(err, service.ErrEdgeNotFound) {
			response.Error(c, http.StatusNotFound, "edge not found")
			return
		}
		if errors.Is(err, service.ErrUnsupportedEdgeAccountOp) {
			response.Error(c, http.StatusBadRequest, "unsupported edge account op")
			return
		}
		// Edge unreachable / request build failure — isolate as bad gateway, never
		// a prod-side 500 that masks "the edge call failed".
		response.Error(c, http.StatusBadGateway, "edge request failed")
		return
	}

	// Relay the edge's response verbatim: it is already the standard credential-free
	// {code,message,data} envelope the rest of the admin UI consumes.
	contentType := "application/json; charset=utf-8"
	c.Data(status, contentType, respBody)
}
