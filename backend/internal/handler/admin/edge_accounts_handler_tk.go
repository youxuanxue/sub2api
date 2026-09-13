package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// edgeAccountsAggregator is the narrow dependency the handler needs.
// *service.EdgeAccountsAggregator satisfies it.
type edgeAccountsAggregator interface {
	Aggregate(ctx context.Context, platform string) (*service.EdgeAccountsAggregate, error)
	AggregateFresh(ctx context.Context, platform string) (*service.EdgeAccountsAggregate, error)
	AggregateByStub(ctx context.Context) (*service.EdgeAccountsAggregate, error)
	AggregateByStubFresh(ctx context.Context) (*service.EdgeAccountsAggregate, error)
	HandoffTarget(ctx context.Context, edgeID string) (*service.EdgeHandoffTarget, error)
	MintAdminSession(ctx context.Context, edgeID string, initiator int64, input service.EdgeHandoffRequest) (*service.EdgeAdminSession, error)
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
	ctx := c.Request.Context()
	var (
		agg *service.EdgeAccountsAggregate
		err error
	)
	// view=by-stub → the inline /accounts panel's per-stub inventory: every prod
	// mirror stub (any platform) fanned out with ITS OWN api-key, so each result is
	// that key's group-scoped accounts (precise correspondence), keyed by stub id.
	// This path is the prod /accounts embedded view of edge runtime state, so it
	// always performs a fresh fan-out before ETag comparison; otherwise a changed
	// edge account can remain hidden behind prod's SWR cache. Default → the
	// standalone per-edge fleet overview, narrowed by ?platform=, where passive
	// polling can keep using the SWR cache unless force=true.
	force := truthyQuery(c.Query("force"))
	if strings.EqualFold(strings.TrimSpace(c.Query("view")), "by-stub") {
		agg, err = h.aggregator.AggregateByStubFresh(ctx)
	} else {
		platform := strings.ToLower(strings.TrimSpace(c.DefaultQuery("platform", "all")))
		if force {
			agg, err = h.aggregator.AggregateFresh(ctx, platform)
		} else {
			agg, err = h.aggregator.Aggregate(ctx, platform)
		}
	}
	if err != nil {
		response.Error(c, 500, "failed to aggregate edge accounts")
		return
	}

	// ETag/304 so the page's periodic auto-refresh skips the body (and the
	// frontend skips a re-render) when nothing changed. Mirrors the admin accounts
	// list (handler.buildAccountsListETag / ifNoneMatchMatched, same package).
	if etag := buildEdgeAccountsETag(agg); etag != "" {
		c.Header("ETag", etag)
		c.Header("Vary", "If-None-Match")
		if ifNoneMatchMatched(c.GetHeader("If-None-Match"), etag) {
			c.Status(http.StatusNotModified)
			return
		}
	}
	response.Success(c, agg)
}

func truthyQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// buildEdgeAccountsETag hashes the aggregate's stable content — Platform + Edges —
// deliberately EXCLUDING EdgeAccountsAggregate.TS, which is the per-fan-out wall
// clock (time.Now().Unix()) and would otherwise churn the ETag on every refresh
// even when the account inventory is byte-identical, defeating the 304 path.
// Mirrors handler.buildAccountsListETag (account_handler.go).
func buildEdgeAccountsETag(agg *service.EdgeAccountsAggregate) string {
	if agg == nil {
		return ""
	}
	payload := struct {
		Platform string                       `json:"platform"`
		Edges    []service.EdgeAccountsResult `json:"edges"`
	}{
		Platform: agg.Platform,
		Edges:    agg.Edges,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

// HandoffTarget exposes no credential and remains behind the existing admin guard.
func (h *EdgeAccountsHandler) HandoffTarget(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h == nil || h.aggregator == nil {
		handoffError(c, service.ErrEdgeHandoffUnavailable)
		return
	}
	target, err := h.aggregator.HandoffTarget(c.Request.Context(), c.Param("edge"))
	if err != nil {
		handoffError(c, err)
		return
	}
	response.Success(c, target)
}
func handoffError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrEdgeNotFound) {
		response.Error(c, http.StatusNotFound, "edge not found")
		return
	}
	if errors.Is(err, service.ErrEdgeHandoffInvalid) {
		response.Error(c, http.StatusBadRequest, "invalid handoff request")
		return
	}
	// Caddy treats 502/504 as a failed gateway instance; this is only an admin feature failure.
	response.Error(c, http.StatusServiceUnavailable, "edge handoff unavailable")
}
func (h *EdgeAccountsHandler) MintAdminSession(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 || c.GetString("auth_method") != "jwt" {
		response.Error(c, http.StatusUnauthorized, "administrator session required")
		return
	}
	edgeID := strings.TrimSpace(c.Param("edge"))
	if edgeID == "" {
		response.Error(c, http.StatusBadRequest, "edge id required")
		return
	}
	var input service.EdgeHandoffRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if c.ShouldBindJSON(&input) != nil || !service.EdgeHandoffProof(input.Challenge) || !service.EdgeHandoffProof(input.Attempt) {
		response.Error(c, http.StatusBadRequest, "invalid handoff request")
		return
	}
	if h == nil || h.aggregator == nil {
		handoffError(c, service.ErrEdgeHandoffUnavailable)
		return
	}
	session, err := h.aggregator.MintAdminSession(c.Request.Context(), edgeID, subject.UserID, input)
	if err != nil {
		handoffError(c, err)
		return
	}
	response.Success(c, session)
}
