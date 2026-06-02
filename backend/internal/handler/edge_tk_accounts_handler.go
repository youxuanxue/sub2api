package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// edgeAccountsReader is the narrow read-only dependency the edge accounts
// endpoint needs. service.AccountRepository satisfies it via ListByPlatform.
// Reusing the existing repository method means NO change to the AccountRepository
// interface — and therefore zero churn on its mocks/stubs (CLAUDE.md rule 6).
type edgeAccountsReader interface {
	ListByPlatform(ctx context.Context, platform string) ([]service.Account, error)
}

// edgeAccountsSupportedPlatforms is the allowlist this read endpoint accepts.
// Edges are anthropic-centric today; the gate keeps a prod misconfig loud
// (400) rather than silently returning an empty list for a typo'd platform.
// Mirrors the capacity endpoint's "reject unsupported, never default silently"
// posture (see edge_tk_capacity_handler.go).
var edgeAccountsSupportedPlatforms = map[string]struct{}{
	service.PlatformAnthropic:   {},
	service.PlatformOpenAI:      {},
	service.PlatformGemini:      {},
	service.PlatformAntigravity: {},
}

// EdgeAccountsHandler serves the TokenKey read-only "edge accounts" endpoint
// that prod's cross-edge admin overview calls over HTTP to enumerate each
// edge's account inventory. It is the list sibling of EdgeCapacityHandler.
//
// Like the capacity endpoint it is mounted behind the dedicated lightweight
// api-key check (middleware/edge_capacity_auth_tk.go), NOT the gateway
// billing/concurrency chain — it is a side-effect-free read.
//
// CREDENTIALS ARE NEVER EXPOSED: the response DTO (edgeAccountDTO) is built
// field-by-field from a non-sensitive allowlist and has no Credentials / Extra
// / Proxy / Notes member at all, so leakage is structurally impossible rather
// than merely redacted. The edge_tk_accounts_handler_test.go asserts the raw
// bytes carry no credential substrings.
type EdgeAccountsHandler struct {
	accounts edgeAccountsReader
}

// NewEdgeAccountsHandler wires the edge accounts handler.
func NewEdgeAccountsHandler(accounts edgeAccountsReader) *EdgeAccountsHandler {
	return &EdgeAccountsHandler{accounts: accounts}
}

// edgeAccountDTO is the on-the-wire, sanitized read-model for one edge account.
// It deliberately omits every credential-bearing field. Timestamps marshal as
// RFC3339 (nil → omitted). Optional anthropic-tier scalars are omitempty so the
// payload stays small for non-anthropic accounts.
type edgeAccountDTO struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Platform       string  `json:"platform"`
	Type           string  `json:"type"`
	ChannelType    int     `json:"channel_type,omitempty"`
	Status         string  `json:"status"`
	Schedulable    bool    `json:"schedulable"`
	IsSchedulable  bool    `json:"is_schedulable"`
	Concurrency    int     `json:"concurrency"`
	Priority       int     `json:"priority"`
	RateMultiplier float64 `json:"rate_multiplier"`
	ErrorMessage   string  `json:"error_message,omitempty"`

	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`

	SessionWindowStatus string     `json:"session_window_status,omitempty"`
	SessionWindowEnd    *time.Time `json:"session_window_end,omitempty"`

	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until,omitempty"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason,omitempty"`

	RateLimitedAt    *time.Time `json:"rate_limited_at,omitempty"`
	RateLimitResetAt *time.Time `json:"rate_limit_reset_at,omitempty"`
	OverloadUntil    *time.Time `json:"overload_until,omitempty"`

	WindowCostLimit float64 `json:"window_cost_limit,omitempty"`
	MaxSessions     int     `json:"max_sessions,omitempty"`
	BaseRPM         int     `json:"base_rpm,omitempty"`

	TierID *int64   `json:"tier_id,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// edgeAccountsResponse is the data envelope returned to the prod aggregator.
type edgeAccountsResponse struct {
	Platform string           `json:"platform"`
	Accounts []edgeAccountDTO `json:"accounts"`
	TS       int64            `json:"ts"`
}

// ListAccounts handles GET /api/v1/edge/accounts?platform=anthropic.
func (h *EdgeAccountsHandler) ListAccounts(c *gin.Context) {
	if h == nil || h.accounts == nil {
		response.Error(c, http.StatusInternalServerError, "edge accounts handler unavailable")
		return
	}

	platform := strings.ToLower(strings.TrimSpace(c.DefaultQuery("platform", service.PlatformAnthropic)))
	if _, ok := edgeAccountsSupportedPlatforms[platform]; !ok {
		response.Error(c, http.StatusBadRequest, "unsupported platform")
		return
	}

	accounts, err := h.accounts.ListByPlatform(c.Request.Context(), platform)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to list accounts")
		return
	}

	dtos := make([]edgeAccountDTO, 0, len(accounts))
	for i := range accounts {
		dtos = append(dtos, toEdgeAccountDTO(&accounts[i]))
	}

	response.Success(c, edgeAccountsResponse{
		Platform: platform,
		Accounts: dtos,
		TS:       time.Now().Unix(),
	})
}

// toEdgeAccountDTO maps a service.Account to the sanitized read-model. It reads
// ONLY non-sensitive fields/getters — Credentials/Extra/Proxy/Notes are never
// touched. The anthropic window/session/rpm scalars come from Extra-backed
// getters and are 0 (→ omitted) for platforms that don't use them.
func toEdgeAccountDTO(a *service.Account) edgeAccountDTO {
	dto := edgeAccountDTO{
		ID:                      a.ID,
		Name:                    a.Name,
		Platform:                a.Platform,
		Type:                    a.Type,
		ChannelType:             a.ChannelType,
		Status:                  a.Status,
		Schedulable:             a.Schedulable,
		IsSchedulable:           a.IsSchedulable(),
		Concurrency:             a.Concurrency,
		Priority:                a.Priority,
		RateMultiplier:          a.BillingRateMultiplier(),
		ErrorMessage:            a.ErrorMessage,
		LastUsedAt:              a.LastUsedAt,
		ExpiresAt:               a.ExpiresAt,
		CreatedAt:               a.CreatedAt,
		SessionWindowStatus:     a.SessionWindowStatus,
		SessionWindowEnd:        a.SessionWindowEnd,
		TempUnschedulableUntil:  a.TempUnschedulableUntil,
		TempUnschedulableReason: a.TempUnschedulableReason,
		RateLimitedAt:           a.RateLimitedAt,
		RateLimitResetAt:        a.RateLimitResetAt,
		OverloadUntil:           a.OverloadUntil,
		WindowCostLimit:         a.GetWindowCostLimit(),
		MaxSessions:             a.GetMaxSessions(),
		BaseRPM:                 a.GetBaseRPM(),
		TierID:                  a.TierID,
	}
	for _, g := range a.Groups {
		if g != nil && strings.TrimSpace(g.Name) != "" {
			dto.Groups = append(dto.Groups, g.Name)
		}
	}
	return dto
}
