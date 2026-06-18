package handler

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

// edgeAccountsMaxPageSize bounds the single-page listing. Edges host a handful
// of operator-curated accounts, so one large page returns the whole inventory.
const edgeAccountsMaxPageSize = 1000

// edgeAccountsLister is the narrow read-only dependency the edge accounts
// endpoint needs. service.AdminService satisfies it via ListAccounts.
//
// It MUST be ListAccounts (status filter = ""), NOT the repository's
// ListByPlatform: ListByPlatform pins status = "active" and therefore hides
// disabled / errored accounts, making this endpoint show fewer rows than the
// edge's own /admin/accounts page. The prod overview must mirror that page's
// full inventory, so it reuses the exact same lister the admin page uses.
type edgeAccountsLister interface {
	ListAccounts(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode, sortBy, sortOrder string) ([]service.Account, int64, error)
}

// edgeAccountsAllPlatforms is the sentinel that returns every platform's
// accounts in one read (the overview's default). It maps to an empty platform
// filter in ListAccounts — see edgeAccountsListFilter.
const edgeAccountsAllPlatforms = "all"

// edgeAccountsSupportedPlatforms is the allowlist this read endpoint accepts.
// The gate keeps a prod misconfig loud (400) rather than silently returning an
// empty list for a typo'd platform. "all" returns the full cross-platform
// inventory the prod overview shows by default; the per-platform values let the
// UI narrow to a single platform.
var edgeAccountsSupportedPlatforms = map[string]struct{}{
	edgeAccountsAllPlatforms:    {},
	service.PlatformAnthropic:   {},
	service.PlatformOpenAI:      {},
	service.PlatformGemini:      {},
	service.PlatformAntigravity: {},
	service.PlatformNewAPI:      {},
	service.PlatformKiro:        {},
}

// edgeAccountsListFilter maps the requested platform to the ListAccounts filter
// value: the "all" sentinel becomes "" (no platform filter → every platform),
// any concrete platform passes through unchanged.
func edgeAccountsListFilter(platform string) string {
	if platform == edgeAccountsAllPlatforms {
		return ""
	}
	return platform
}

// The runtime-gauge readers below mirror the dependencies admin AccountHandler
// uses to enrich its list (see handler/admin/account_handler.go List() — the
// reference block this endpoint replicates). Each is OPTIONAL (nil-safe): a nil
// reader simply skips that gauge so the endpoint degrades to the static fields
// rather than failing. They read THIS edge's local Redis/DB, which is exactly
// the per-edge live state the prod overview wants to surface.
type edgeConcurrencyReader interface {
	GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

type edgeSessionReader interface {
	GetActiveSessionCountBatch(ctx context.Context, accountIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error)
}

type edgeRPMReader interface {
	GetRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

type edgeUsageReader interface {
	GetAccountWindowStats(ctx context.Context, accountID int64, startTime time.Time) (*usagestats.AccountStats, error)
	GetTodayStatsBatch(ctx context.Context, accountIDs []int64) (map[int64]*service.WindowStats, error)
	// GetPassiveUsage builds the 5h/7d usage windows from the account's persisted
	// passive samples (Extra), with NO upstream Anthropic API call — the same
	// "被动采样" source the per-edge admin page shows.
	GetPassiveUsage(ctx context.Context, accountID int64) (*service.UsageInfo, error)
}

// EdgeAccountsHandler serves the TokenKey read-only "edge accounts" endpoint
// that prod's cross-edge admin overview calls over HTTP to enumerate each
// edge's account inventory + live capacity/today gauges. It is the list sibling
// of EdgeCapacityHandler.
//
// Like the capacity endpoint it is mounted behind the dedicated lightweight
// api-key check (middleware/edge_capacity_auth_tk.go), NOT the gateway
// billing/concurrency chain — it is a side-effect-free read.
//
// CREDENTIALS ARE NEVER EXPOSED: the response DTO (edgeAccountDTO) is built
// field-by-field from a non-sensitive allowlist and has no Credentials / Extra
// / Proxy member at all, so leakage is structurally impossible rather than
// merely redacted. The single operator-text field it does carry is Notes (the
// 备注 the admin accounts page shows) — a non-credential remark, surfaced so the
// overview's name cell matches that page. The edge_tk_accounts_handler_test.go
// asserts the raw bytes carry no credential substrings.
type EdgeAccountsHandler struct {
	accounts    edgeAccountsLister
	concurrency edgeConcurrencyReader
	sessions    edgeSessionReader
	rpm         edgeRPMReader
	usage       edgeUsageReader
}

// NewEdgeAccountsHandler wires the edge accounts handler. The runtime-gauge
// readers may be nil (the endpoint then returns static fields only).
func NewEdgeAccountsHandler(
	accounts edgeAccountsLister,
	concurrency edgeConcurrencyReader,
	sessions edgeSessionReader,
	rpm edgeRPMReader,
	usage edgeUsageReader,
) *EdgeAccountsHandler {
	return &EdgeAccountsHandler{
		accounts:    accounts,
		concurrency: concurrency,
		sessions:    sessions,
		rpm:         rpm,
		usage:       usage,
	}
}

// edgeTodayStats mirrors service.WindowStats minus standard_cost (the overview
// shows account cost "A" and user cost "U", matching AccountTodayStatsCell).
type edgeTodayStats struct {
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	UserCost float64 `json:"user_cost"`
}

// edgeAccountDTO is the on-the-wire, sanitized read-model for one edge account.
// It deliberately omits every credential-bearing field. Timestamps marshal as
// RFC3339 (nil → omitted). The current_* / today_stats fields are live gauges
// computed from this edge's local Redis/DB; the *_limit / max_* / base_* fields
// are the configured caps the gauges render against.
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
	// Notes is the operator 备注 (admin remark), mirroring the admin accounts
	// page's name cell. Non-credential; nil when unset.
	Notes *string `json:"notes,omitempty"`

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

	// Configured caps (anthropic oauth/setup-token).
	WindowCostLimit           float64 `json:"window_cost_limit,omitempty"`
	WindowCostStickyReserve   float64 `json:"window_cost_sticky_reserve,omitempty"`
	MaxSessions               int     `json:"max_sessions,omitempty"`
	SessionIdleTimeoutMinutes int     `json:"session_idle_timeout_minutes,omitempty"`
	BaseRPM                   int     `json:"base_rpm,omitempty"`
	RPMStrategy               string  `json:"rpm_strategy,omitempty"`
	RPMStickyBuffer           int     `json:"rpm_sticky_buffer,omitempty"`

	// Live gauges (this edge's local Redis/DB). Pointers so "feature off" (nil)
	// is distinguishable from a real 0; current_concurrency is always present.
	CurrentConcurrency int             `json:"current_concurrency"`
	CurrentWindowCost  *float64        `json:"current_window_cost,omitempty"`
	ActiveSessions     *int            `json:"active_sessions,omitempty"`
	CurrentRPM         *int            `json:"current_rpm,omitempty"`
	TodayStats         *edgeTodayStats `json:"today_stats,omitempty"`

	// Passive 5h/7d usage windows (anthropic oauth/setup-token). Source is always
	// "passive" — read from persisted Extra samples, no upstream API call.
	Usage *edgeUsageWindows `json:"usage,omitempty"`

	TierID *int64   `json:"tier_id,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// edgeUsageWindows mirrors the minimal subset of service.UsageInfo the usage
// cell reads (utilization + reset per window); window_stats is supplied
// frontend-side from today_stats, so it is not duplicated here.
type edgeUsageWindows struct {
	Source         string             `json:"source"`
	FiveHour       *edgeUsageProgress `json:"five_hour,omitempty"`
	SevenDay       *edgeUsageProgress `json:"seven_day,omitempty"`
	SevenDaySonnet *edgeUsageProgress `json:"seven_day_sonnet,omitempty"`
}

type edgeUsageProgress struct {
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
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

	platform := strings.ToLower(strings.TrimSpace(c.DefaultQuery("platform", edgeAccountsAllPlatforms)))
	if _, ok := edgeAccountsSupportedPlatforms[platform]; !ok {
		response.Error(c, http.StatusBadRequest, "unsupported platform")
		return
	}

	ctx := c.Request.Context()
	// status="" → all statuses (active/disabled/errored), matching the edge's own
	// /admin/accounts page. priority asc mirrors the admin default ordering.
	// platform="all" → "" filter (every platform); a concrete platform narrows.
	accounts, _, err := h.accounts.ListAccounts(ctx, 1, edgeAccountsMaxPageSize, edgeAccountsListFilter(platform), "", "", "", 0, "", "priority", "asc")
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "failed to list accounts")
		return
	}

	runtime := h.collectRuntimeGauges(ctx, accounts)

	dtos := make([]edgeAccountDTO, 0, len(accounts))
	for i := range accounts {
		dto := toEdgeAccountDTO(&accounts[i])
		runtime.apply(&accounts[i], &dto)
		dtos = append(dtos, dto)
	}

	response.Success(c, edgeAccountsResponse{
		Platform: platform,
		Accounts: dtos,
		TS:       time.Now().Unix(),
	})
}

// edgeRuntimeGauges holds the batch-collected live values keyed by account id.
type edgeRuntimeGauges struct {
	concurrency  map[int64]int
	windowCost   map[int64]float64
	sessions     map[int64]int
	rpm          map[int64]int
	today        map[int64]*service.WindowStats
	usageWindows map[int64]*service.UsageInfo
}

// apply copies the per-account gauges onto the DTO, mirroring the admin
// AccountWithConcurrency assembly (current_* only set when the feature applies).
func (g *edgeRuntimeGauges) apply(acc *service.Account, dto *edgeAccountDTO) {
	if g == nil {
		return
	}
	dto.CurrentConcurrency = g.concurrency[acc.ID]
	if g.windowCost != nil {
		if cost, ok := g.windowCost[acc.ID]; ok {
			dto.CurrentWindowCost = &cost
		}
	}
	if g.sessions != nil {
		if n, ok := g.sessions[acc.ID]; ok {
			dto.ActiveSessions = &n
		}
	}
	if g.rpm != nil {
		if n, ok := g.rpm[acc.ID]; ok {
			dto.CurrentRPM = &n
		}
	}
	if g.today != nil {
		if ws, ok := g.today[acc.ID]; ok && ws != nil {
			dto.TodayStats = &edgeTodayStats{
				Requests: ws.Requests,
				Tokens:   ws.Tokens,
				Cost:     ws.Cost,
				UserCost: ws.UserCost,
			}
		}
	}
	if g.usageWindows != nil {
		if u, ok := g.usageWindows[acc.ID]; ok && u != nil {
			dto.Usage = toEdgeUsageWindows(u)
		}
	}
}

// toEdgeUsageWindows maps the passive UsageInfo to the DTO's window subset.
func toEdgeUsageWindows(u *service.UsageInfo) *edgeUsageWindows {
	w := &edgeUsageWindows{Source: u.Source}
	if u.FiveHour != nil {
		w.FiveHour = &edgeUsageProgress{Utilization: u.FiveHour.Utilization, ResetsAt: u.FiveHour.ResetsAt}
	}
	if u.SevenDay != nil {
		w.SevenDay = &edgeUsageProgress{Utilization: u.SevenDay.Utilization, ResetsAt: u.SevenDay.ResetsAt}
	}
	if u.SevenDaySonnet != nil {
		w.SevenDaySonnet = &edgeUsageProgress{Utilization: u.SevenDaySonnet.Utilization, ResetsAt: u.SevenDaySonnet.ResetsAt}
	}
	if w.FiveHour == nil && w.SevenDay == nil && w.SevenDaySonnet == nil {
		return nil
	}
	return w
}

// collectRuntimeGauges batch-reads the live capacity/today gauges for the given
// accounts from this edge's local Redis/DB. It replicates the gating and batch
// strategy of admin AccountHandler.List (account_handler.go:278-386): concurrency
// + today-stats for all accounts; window-cost / sessions / rpm only for anthropic
// OAuth/setup-token accounts with the corresponding cap configured. Every reader
// is nil-safe and partial failure is swallowed (the gauge is simply absent).
func (h *EdgeAccountsHandler) collectRuntimeGauges(ctx context.Context, accounts []service.Account) *edgeRuntimeGauges {
	g := &edgeRuntimeGauges{concurrency: map[int64]int{}}
	if len(accounts) == 0 {
		return g
	}

	accountIDs := make([]int64, len(accounts))
	for i := range accounts {
		accountIDs[i] = accounts[i].ID
	}

	// Concurrency: cheap Redis ZCARD, all accounts.
	if h.concurrency != nil {
		if cc, err := h.concurrency.GetAccountConcurrencyBatch(ctx, accountIDs); err == nil && cc != nil {
			g.concurrency = cc
		}
	}

	// Today stats: batch SQL, all accounts.
	if h.usage != nil {
		if ts, err := h.usage.GetTodayStatsBatch(ctx, accountIDs); err == nil && ts != nil {
			g.today = ts
		}
	}

	// Gate window-cost / sessions / rpm by anthropic OAuth/setup-token + cap.
	windowCostIDs := make([]int64, 0)
	sessionIDs := make([]int64, 0)
	rpmIDs := make([]int64, 0)
	idleTimeouts := make(map[int64]time.Duration)
	for i := range accounts {
		acc := &accounts[i]
		if !acc.IsAnthropicOAuthOrSetupToken() {
			continue
		}
		if acc.GetWindowCostLimit() > 0 {
			windowCostIDs = append(windowCostIDs, acc.ID)
		}
		if acc.GetMaxSessions() > 0 {
			sessionIDs = append(sessionIDs, acc.ID)
			idleTimeouts[acc.ID] = time.Duration(acc.GetSessionIdleTimeoutMinutes()) * time.Minute
		}
		if acc.GetBaseRPM() > 0 {
			rpmIDs = append(rpmIDs, acc.ID)
		}
	}

	if len(rpmIDs) > 0 && h.rpm != nil {
		if m, err := h.rpm.GetRPMBatch(ctx, rpmIDs); err == nil {
			g.rpm = m
		}
	}
	if len(sessionIDs) > 0 && h.sessions != nil {
		if m, err := h.sessions.GetActiveSessionCountBatch(ctx, sessionIDs, idleTimeouts); err == nil {
			g.sessions = m
		}
	}
	if len(windowCostIDs) > 0 && h.usage != nil {
		g.windowCost = make(map[int64]float64)
		var mu sync.Mutex
		eg, egctx := errgroup.WithContext(ctx)
		eg.SetLimit(10)
		for i := range accounts {
			acc := &accounts[i]
			if !acc.IsAnthropicOAuthOrSetupToken() || acc.GetWindowCostLimit() <= 0 {
				continue
			}
			accCopy := acc
			eg.Go(func() error {
				startTime := accCopy.GetCurrentWindowStartTime()
				stats, err := h.usage.GetAccountWindowStats(egctx, accCopy.ID, startTime)
				if err == nil && stats != nil {
					mu.Lock()
					g.windowCost[accCopy.ID] = stats.StandardCost
					mu.Unlock()
				}
				return nil // partial failure tolerated
			})
		}
		_ = eg.Wait()
	}

	// Passive 5h/7d usage windows: read from persisted Extra samples (no upstream
	// API). anthropic OAuth/setup-token rebuild from passive_usage_* samples;
	// openai OAuth (codex) rebuild from codex_*_used_percent samples. Both go
	// through GetPassiveUsage, which dispatches by platform. Other platforms have
	// no passive window source here, so they are skipped (the cell shows "-").
	// errgroup-bounded like window-cost.
	if h.usage != nil {
		var mu sync.Mutex
		eg, egctx := errgroup.WithContext(ctx)
		eg.SetLimit(10)
		usage := make(map[int64]*service.UsageInfo)
		for i := range accounts {
			acc := &accounts[i]
			if !acc.IsAnthropicOAuthOrSetupToken() && !acc.IsOpenAIOAuth() {
				continue
			}
			id := acc.ID
			eg.Go(func() error {
				info, err := h.usage.GetPassiveUsage(egctx, id)
				if err == nil && info != nil {
					mu.Lock()
					usage[id] = info
					mu.Unlock()
				}
				return nil // partial failure tolerated
			})
		}
		_ = eg.Wait()
		if len(usage) > 0 {
			g.usageWindows = usage
		}
	}

	return g
}

// toEdgeAccountDTO maps a service.Account to the sanitized read-model's static
// fields. It reads ONLY non-sensitive fields/getters — Credentials/Extra/Proxy/
// Notes are never touched. The live current_* gauges are attached separately by
// edgeRuntimeGauges.apply.
func toEdgeAccountDTO(a *service.Account) edgeAccountDTO {
	dto := edgeAccountDTO{
		ID:                        a.ID,
		Name:                      a.Name,
		Platform:                  a.Platform,
		Type:                      a.Type,
		ChannelType:               a.ChannelType,
		Status:                    a.Status,
		Schedulable:               a.Schedulable,
		IsSchedulable:             a.IsSchedulable(),
		Concurrency:               a.Concurrency,
		Priority:                  a.Priority,
		RateMultiplier:            a.BillingRateMultiplier(),
		ErrorMessage:              a.ErrorMessage,
		Notes:                     a.Notes,
		LastUsedAt:                a.LastUsedAt,
		ExpiresAt:                 a.ExpiresAt,
		CreatedAt:                 a.CreatedAt,
		SessionWindowStatus:       a.SessionWindowStatus,
		SessionWindowEnd:          a.SessionWindowEnd,
		TempUnschedulableUntil:    a.TempUnschedulableUntil,
		TempUnschedulableReason:   a.TempUnschedulableReason,
		RateLimitedAt:             a.RateLimitedAt,
		RateLimitResetAt:          a.RateLimitResetAt,
		OverloadUntil:             a.OverloadUntil,
		WindowCostLimit:           a.GetWindowCostLimit(),
		WindowCostStickyReserve:   a.GetWindowCostStickyReserve(),
		MaxSessions:               a.GetMaxSessions(),
		SessionIdleTimeoutMinutes: a.GetSessionIdleTimeoutMinutes(),
		BaseRPM:                   a.GetBaseRPM(),
		RPMStrategy:               a.GetRPMStrategy(),
		RPMStickyBuffer:           a.GetRPMStickyBuffer(),
		TierID:                    a.TierID,
	}
	for _, grp := range a.Groups {
		if grp != nil && strings.TrimSpace(grp.Name) != "" {
			dto.Groups = append(dto.Groups, grp.Name)
		}
	}
	return dto
}
