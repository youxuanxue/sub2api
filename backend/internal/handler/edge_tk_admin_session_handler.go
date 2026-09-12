package handler

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"log/slog"
	"net/http"
)

type userByIDLookup interface {
	GetByID(context.Context, int64) (*service.User, error)
}
type edgeAdminSessionMinter interface {
	GenerateEdgeAdminSessionTokenPair(context.Context, *service.User, string) (*service.TokenPair, error)
}
type EdgeAdminSessionHandler struct {
	handoff *service.EdgeAdminHandoff
	users   userByIDLookup
	minter  edgeAdminSessionMinter
}

func NewEdgeAdminSessionHandler(handoff *service.EdgeAdminHandoff, users userByIDLookup, minter edgeAdminSessionMinter) *EdgeAdminSessionHandler {
	return &EdgeAdminSessionHandler{handoff: handoff, users: users, minter: minter}
}

// Mint permanently retires the mirror-key session mint. Never fall back to it.
func (h *EdgeAdminSessionHandler) Mint(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	response.Error(c, http.StatusGone, "use proof-bound edge handoff or sign in")
}
func (h *EdgeAdminSessionHandler) Configuration(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	receiver := h.handoff.Receiver()
	if receiver == nil {
		response.Error(c, http.StatusServiceUnavailable, "edge handoff unavailable")
		return
	}
	response.Success(c, gin.H{"issuer": receiver.Issuer, "origin": receiver.Origin})
}
func (h *EdgeAdminSessionHandler) MintCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	var input service.EdgeHandoffDelegation
	if c.ContentType() != "application/json" || c.ShouldBindJSON(&input) != nil {
		response.Error(c, http.StatusBadRequest, "invalid handoff request")
		return
	}
	result, err := h.handoff.Mint(c.Request.Context(), input)
	if err != nil {
		response.Error(c, http.StatusForbidden, "handoff unavailable or invalid")
		return
	}
	response.Success(c, result)
}
func (h *EdgeAdminSessionHandler) Exchange(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	receiver := h.handoff.Receiver()
	if receiver == nil || h.users == nil || h.minter == nil {
		response.Error(c, http.StatusServiceUnavailable, "edge handoff unavailable")
		return
	}
	if c.GetHeader("Origin") != receiver.Origin || c.ContentType() != "application/json" {
		response.Error(c, http.StatusForbidden, "same-origin exchange required")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input service.EdgeHandoffExchange
	if c.ShouldBindJSON(&input) != nil {
		response.Error(c, http.StatusBadRequest, "invalid handoff request")
		return
	}
	claims, err := h.handoff.Exchange(c.Request.Context(), input)
	if err != nil {
		response.Error(c, http.StatusForbidden, "invalid or expired handoff")
		return
	}
	user, err := h.users.GetByID(c.Request.Context(), receiver.AdminUserID)
	if err != nil || user == nil || !user.IsAdmin() || !user.IsActive() {
		response.Error(c, http.StatusForbidden, "edge administrator unavailable")
		return
	}
	family := service.EdgeHandoffFamily(claims)
	pair, err := h.minter.GenerateEdgeAdminSessionTokenPair(c.Request.Context(), user, family)
	if err != nil || pair == nil {
		response.Error(c, http.StatusServiceUnavailable, "could not establish edge session")
		return
	}
	slog.Info("edge_admin_handoff", "initiator", claims.Initiator, "issuer", claims.Issuer, "edge_user_id", user.ID, "attempt", claims.Attempt, "family", family, "key_id", claims.KeyID)
	response.Success(c, pair)
}
