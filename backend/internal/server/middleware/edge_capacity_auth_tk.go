package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// edgeCapacityKeyLookup is the narrow read-only api-key dependency the edge
// capacity middleware needs. *service.APIKeyService satisfies it.
type edgeCapacityKeyLookup interface {
	GetByKey(ctx context.Context, key string) (*service.APIKey, error)
}

// EdgeCallerAPIKeyCtxKey is the gin context key under which this middleware stashes
// the authenticated caller *service.APIKey. A downstream edge handler reads it to
// scope a read to exactly that key's group (the per-stub panel's precise
// correspondence — see EdgeAccountsHandler.ListAccounts group_scope=caller) without
// re-looking-up the key. Read-only; the key never leaves the edge.
const EdgeCallerAPIKeyCtxKey = "edge_caller_apikey"

// NewEdgeCapacityAuthMiddleware authenticates the TokenKey edge capacity endpoint
// with a DELIBERATELY minimal check: the request must present an x-api-key (or
// Bearer) that resolves to an active api-key on THIS deployment. It does NOT run
// the gateway's billing / quota / subscription / concurrency / rate-limit chain
// — the endpoint is a side-effect-free read, and the prod reconciler already
// holds a valid relay api-key for the edge it points at (zero new secret).
//
// This is the primary auth layer. An optional Caddy remote_ip allowlist may sit
// in front as defense-in-depth, but it is NOT the only layer: prod egress IPs
// rotate as routine fleet ops, so a pure-IP gate would become a silent failure
// source. See plan Stage 3 / docs reconciler notes.
func NewEdgeCapacityAuthMiddleware(lookup edgeCapacityKeyLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		if lookup == nil {
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "edge capacity auth unavailable")
			return
		}

		keyString := extractEdgeCapacityKey(c)
		if keyString == "" {
			AbortWithError(c, http.StatusUnauthorized, "API_KEY_REQUIRED", "API key is required (x-api-key header or Bearer token)")
			return
		}

		apiKey, err := lookup.GetByKey(c.Request.Context(), keyString)
		if err != nil {
			if errors.Is(err, service.ErrAPIKeyNotFound) {
				AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
				return
			}
			if IsClientClosedRequestError(c, err) {
				AbortClientClosedRequest(c, err)
				return
			}
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to validate API key")
			return
		}
		// Reject keys that are not usable for any reason other than expiry /
		// quota (those are billing concerns the capacity read does not care
		// about, but a disabled/unknown key must never read fleet capacity).
		if apiKey == nil || (!apiKey.IsActive() &&
			apiKey.Status != service.StatusAPIKeyExpired &&
			apiKey.Status != service.StatusAPIKeyQuotaExhausted) {
			AbortWithError(c, http.StatusUnauthorized, "API_KEY_DISABLED", "API key is disabled")
			return
		}

		// Expose the authenticated caller key for group-scoped reads (see const doc).
		c.Set(EdgeCallerAPIKeyCtxKey, apiKey)
		c.Next()
	}
}

// extractEdgeCapacityKey pulls the api-key from Authorization (Bearer) or the
// x-api-key header, mirroring the gateway middleware's extraction order without
// its query-param deprecation path (the capacity endpoint is internal-only).
func extractEdgeCapacityKey(c *gin.Context) string {
	if authHeader := c.GetHeader("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); k != "" {
				return k
			}
		}
	}
	return strings.TrimSpace(c.GetHeader("x-api-key"))
}
