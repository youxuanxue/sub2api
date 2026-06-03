package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// edgeAccountsAggregator is the narrow dependency the handler needs.
// *service.EdgeAccountsAggregator satisfies it.
type edgeAccountsAggregator interface {
	Aggregate(ctx context.Context, platform string) (*service.EdgeAccountsAggregate, error)
	MintAdminSession(ctx context.Context, edgeID string) (*service.EdgeAdminSession, error)
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

// List GET /api/v1/admin/edge-accounts?platform=all
//
// Defaults to "all" — every platform's accounts across the fleet — so the
// overview is complete by default; a concrete ?platform= narrows to one.
// Per-edge failures are carried inside the payload (edges[].ok / .error); a 500
// is only returned when discovery itself fails (e.g. the local account list read
// or the baseline regex load).
func (h *EdgeAccountsHandler) List(c *gin.Context) {
	if h == nil || h.aggregator == nil {
		response.Error(c, 500, "edge accounts handler unavailable")
		return
	}
	platform := strings.ToLower(strings.TrimSpace(c.DefaultQuery("platform", "all")))
	agg, err := h.aggregator.Aggregate(c.Request.Context(), platform)
	if err != nil {
		response.Error(c, 500, "failed to aggregate edge accounts")
		return
	}
	response.Success(c, agg)
}

// adminSessionResponse is returned to the prod admin UI: a ready-to-open handoff
// URL on the target edge that auto-logs-in and lands on its /admin/accounts page.
type adminSessionResponse struct {
	EdgeID     string `json:"edge_id"`
	HandoffURL string `json:"handoff_url"`
	ExpiresIn  int    `json:"expires_in"`
}

// MintAdminSession POST /api/v1/admin/edge-accounts/:edge/admin-session
//
// Forwards to the target edge to mint a short-lived admin JWT (using the
// mirror-stub api-key prod already holds), then returns a handoff URL the UI
// opens in a new tab. The token rides in the URL FRAGMENT so it never reaches an
// edge access log / Referer. Admin-JWT gated (inherited from the /admin group):
// only a prod admin can drive a cross-edge management jump.
func (h *EdgeAccountsHandler) MintAdminSession(c *gin.Context) {
	if h == nil || h.aggregator == nil {
		response.Error(c, http.StatusInternalServerError, "edge accounts handler unavailable")
		return
	}
	edgeID := strings.ToLower(strings.TrimSpace(c.Param("edge")))
	if edgeID == "" {
		response.Error(c, http.StatusBadRequest, "edge id required")
		return
	}

	session, err := h.aggregator.MintAdminSession(c.Request.Context(), edgeID)
	if err != nil {
		if errors.Is(err, service.ErrEdgeNotFound) {
			response.Error(c, http.StatusNotFound, "edge not found")
			return
		}
		// Edge unreachable / non-2xx / decode failure — isolate as a bad gateway,
		// never a prod-side 500 that masks "the edge said no".
		response.Error(c, http.StatusBadGateway, "failed to mint edge admin session")
		return
	}

	response.Success(c, adminSessionResponse{
		EdgeID:     session.EdgeID,
		HandoffURL: buildEdgeHandoffURL(session.BaseURL, session.Token),
		ExpiresIn:  session.ExpiresIn,
	})
}

// buildEdgeHandoffURL assembles the edge SPA handoff entry. Token + next live in
// the FRAGMENT (after #) so they are never sent to the server, logged, or leaked
// via Referer; the edge's EdgeHandoffView consumes and scrubs them on load.
func buildEdgeHandoffURL(baseURL, token string) string {
	base := strings.TrimRight(baseURL, "/")
	frag := "tk_session=" + url.QueryEscape(token) + "&next=" + url.QueryEscape("/admin/accounts")
	return base + "/admin/edge-handoff#" + frag
}
