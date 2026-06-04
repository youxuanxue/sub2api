package service

// AnthropicConfigReconciler is the per-node, in-process self-healer that drops
// the high-frequency "safe item" writes of ops/anthropic/manage-anthropic-config.py
// down into the backend so a tier change / drift fix no longer needs an operator
// laptop + SSM fan-out. See plan: docs reconciler notes.
//
// Boundaries (held at every step):
//   - It ONLY ever writes THIS deployment's own database. It never pretends to
//     cover the fleet — fleet fan-out stays with the Python pipeline.
//   - Safe items are self-healed (operator Σ, stub pool_mode, edge balance floor,
//     surface-C concurrency mirror). A single account's tier NUMERIC field drift
//     (base_rpm / max_sessions / window — overlaid at runtime from the tiers
//     table) is REPORTED ONLY (slog.Warn) — never silently rewritten, because the
//     tier NUMBER is set explicitly via the admin UI ApplyTier action.
//   - shared_baseline INFRASTRUCTURE, by contrast, IS self-healed (Step baseline):
//     the canonical TLS profile's existence + the account's binding to it, the
//     credentials self-protection template, and the (post-#551 uniform) priority.
//     These are tier-independent and were the root cause of silent built-in-default
//     TLS fallback when an account was created without the profile row present.
//     Self-heal re-runs the SAME complete write path (ApplyTier), so any creation
//     path (admin UI, bare SQL) converges.
//   - surface C (concurrency mirror) NEVER writes 0 on a failed/timed-out/5xx
//     edge read — it skips the stub and leaves the prior value intact.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/baseline"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	anthropicReconcilerLockKey = "anthropic:config:reconciler:leader"
	anthropicReconcilerLockTTL = 4 * time.Minute
	anthropicReconcilerRunTO   = 90 * time.Second
	anthropicReconcilerHTTPTO  = 8 * time.Second

	// Edge operator (users.id=1) balance floor policy. Mirrors
	// deploy/aws/stage0/anthropic-edge-operator-balance-baselines.json — kept as
	// constants (not embedded) because the reconciler writes the floor via the
	// admin balance "set" path, and the two scalars rarely change. If they do,
	// update both here and the deploy JSON.
	anthropicEdgeBalanceFloorThreshold = 100.0
	anthropicEdgeBalanceFloorDefault   = 9999999.0
)

// anthropicReconcilerLockRelease is the compare-and-delete unlock script — only
// the instance that holds the lock may delete it (mirrors the ops alert evaluator).
var anthropicReconcilerLockRelease = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// reconcilerAccountStore is the narrow account dependency. *accountRepository
// (service.AccountRepository) satisfies it; a small interface keeps runOnce
// unit-testable without a full repository stub.
type reconcilerAccountStore interface {
	ListByPlatform(ctx context.Context, platform string) ([]Account, error)
	SumConcurrencyAnthropic(ctx context.Context) (int64, error)
	BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error)
}

// reconcilerUserStore is the narrow user dependency for the operator Σ sync and
// the balance-floor read.
type reconcilerUserStore interface {
	GetByID(ctx context.Context, id int64) (*User, error)
	BatchSetConcurrency(ctx context.Context, userIDs []int64, value int) (int, error)
}

// reconcilerBalanceSetter sets an absolute balance and invalidates the billing
// cache. *adminServiceImpl (AdminService) satisfies it via UpdateUserBalance.
type reconcilerBalanceSetter interface {
	UpdateUserBalance(ctx context.Context, userID int64, balance float64, operation string, notes string) (*User, error)
}

// httpDoer is the minimal HTTP client surface (so tests inject a fake).
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// reconcilerTierResolver resolves a tier's desired concurrency (Step T value-sync)
// and its name (Step baseline self-heal, which re-applies the tier by name).
// *TierService satisfies it; nil-safe (steps no-op when absent).
type reconcilerTierResolver interface {
	ResolveConcurrency(tierID int64) (int, bool)
	ResolveName(tierID int64) (string, bool)
}

// reconcilerTierApplier re-applies a tier onto an account — the single complete
// "materialize shared_baseline" write path (TLS profile ensure+bind, credentials
// template, extra flags, priority, concurrency). *AccountTierService satisfies it.
// nil-safe (Step baseline no-ops when absent).
type reconcilerTierApplier interface {
	ApplyTier(ctx context.Context, accountID int64, tier string) (*Account, error)
}

// reconcilerTLSProfileResolver reads a TLS profile by id so the baseline drift
// check can detect a DANGLING binding (id set but the row was deleted / renamed).
// *TLSFingerprintProfileService satisfies it. nil → that sub-check is skipped.
type reconcilerTLSProfileResolver interface {
	GetByID(ctx context.Context, id int64) (*model.TLSFingerprintProfile, error)
}

// reconcilerMimicrySettings self-heals the deployment-level Claude Code UA +
// mimicry manifest settings toward the embedded baseline (the in-process
// equivalent of ops `sync-runtime`). *SettingService satisfies it. nil → Step UA
// no-ops.
type reconcilerMimicrySettings interface {
	EnsureClaudeCodeMimicryBaseline(ctx context.Context) (bool, error)
}

// AnthropicConfigReconciler runs a single ticker that, each tick, acquires a
// redis leader lock (best-effort; degrades to lockless on no redis) and runs the
// safe-item self-heal + tier-drift report against the local DB.
type AnthropicConfigReconciler struct {
	accounts    reconcilerAccountStore
	users       reconcilerUserStore
	balance     reconcilerBalanceSetter
	tiers       reconcilerTierResolver
	tierApplier reconcilerTierApplier
	tlsProfiles reconcilerTLSProfileResolver
	mimicry     reconcilerMimicrySettings
	cfg         *config.Config
	redis       *redis.Client
	http        httpDoer
	instanceID  string

	stopCh   chan struct{}
	stopOnce sync.Once
	startOne sync.Once
	wg       sync.WaitGroup

	warnNoRedisOnce sync.Once
}

// NewAnthropicConfigReconciler constructs the reconciler. A nil accounts/users
// store produces a no-op on Start, keeping wire wiring safe for minimal test deps.
// tierApplier/tlsProfiles may be nil — the baseline self-heal step degrades to a
// no-op (applier absent) or skips the dangling-binding sub-check (resolver absent).
func NewAnthropicConfigReconciler(
	accounts reconcilerAccountStore,
	users reconcilerUserStore,
	balance reconcilerBalanceSetter,
	tiers reconcilerTierResolver,
	tierApplier reconcilerTierApplier,
	tlsProfiles reconcilerTLSProfileResolver,
	mimicry reconcilerMimicrySettings,
	cfg *config.Config,
	redisClient *redis.Client,
) *AnthropicConfigReconciler {
	return &AnthropicConfigReconciler{
		accounts:    accounts,
		users:       users,
		balance:     balance,
		tiers:       tiers,
		tierApplier: tierApplier,
		tlsProfiles: tlsProfiles,
		mimicry:     mimicry,
		cfg:         cfg,
		redis:       redisClient,
		http:        &http.Client{Timeout: anthropicReconcilerHTTPTO},
		instanceID:  uuid.NewString(),
		stopCh:      make(chan struct{}),
	}
}

// tickInterval mirrors the FullRebuildIntervalSeconds / reaper convention: <=0
// disables the goroutine; viper.SetDefault supplies the non-zero default.
func (r *AnthropicConfigReconciler) tickInterval() time.Duration {
	if r == nil || r.cfg == nil {
		return 0
	}
	sec := r.cfg.Gateway.Scheduling.AnthropicConfigReconcilerIntervalSeconds
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// Start launches the reconciler goroutine. Safe to call once; a nil store or
// zero interval no-ops.
func (r *AnthropicConfigReconciler) Start() {
	if r == nil || r.accounts == nil || r.users == nil {
		return
	}
	interval := r.tickInterval()
	if interval <= 0 {
		return
	}
	r.startOne.Do(func() {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					r.runOnceLocked()
				case <-r.stopCh:
					return
				}
			}
		}()
	})
}

// Stop signals the goroutine to exit and waits. Safe to call once.
func (r *AnthropicConfigReconciler) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	r.wg.Wait()
}

// runOnceLocked acquires the redis leader lock (if redis is configured) and runs
// runOnce. Multiple replicas thus self-heal at most once per tick fleet-wide for
// a given DB; single-node / no-redis runs lockless.
func (r *AnthropicConfigReconciler) runOnceLocked() {
	release, ok := r.tryAcquireLock()
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), anthropicReconcilerRunTO)
	defer cancel()
	r.runOnce(ctx)
}

func (r *AnthropicConfigReconciler) tryAcquireLock() (func(), bool) {
	if r.redis == nil {
		r.warnNoRedisOnce.Do(func() {
			slog.Warn("anthropic config reconciler running without distributed lock (no redis)")
		})
		return nil, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acquired, err := r.redis.SetNX(ctx, anthropicReconcilerLockKey, r.instanceID, anthropicReconcilerLockTTL).Result()
	if err != nil {
		// Fail-closed: skip this cycle rather than risk concurrent self-heal.
		slog.Warn("anthropic config reconciler leader lock SetNX failed; skipping cycle", "err", err)
		return nil, false
	}
	if !acquired {
		return nil, false
	}
	return func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_, _ = anthropicReconcilerLockRelease.Run(releaseCtx, r.redis, []string{anthropicReconcilerLockKey}, r.instanceID).Result()
	}, true
}

// runOnce performs a single self-heal pass against the LOCAL DB. Tests call it
// directly to drive invariants without the ticker / lock.
func (r *AnthropicConfigReconciler) runOnce(ctx context.Context) {
	if r == nil || r.accounts == nil || r.users == nil {
		return
	}

	accounts, err := r.accounts.ListByPlatform(ctx, PlatformAnthropic)
	if err != nil {
		slog.Warn("anthropic config reconciler: list anthropic accounts failed", "err", err)
		return
	}

	// Step B: pool_mode self-heal on prod mirror stubs.
	r.reconcileStubPoolMode(ctx, accounts)

	// Step T: value-sync each tier-bound anthropic OAUTH account's concurrency
	// column from its tier row (the reference-table write-source). Runs before
	// Step A so the operator Σ reflects any concurrency it just re-asserted.
	r.reconcileTierConcurrency(ctx, accounts)

	// Step baseline: self-heal shared_baseline INFRASTRUCTURE (canonical TLS
	// profile existence + binding, credentials self-protection template, uniform
	// priority) for tier-bound anthropic OAuth/setup-token accounts by re-running
	// ApplyTier when drifted. Idempotent; the single complete write path. This is
	// what makes "operator just sets the tier" yield a fully-configured account.
	r.reconcileAccountBaselineDrift(ctx, accounts)

	// Step C: surface-C concurrency mirror (prod). Runs before the operator Σ
	// sync so the Σ reflects any concurrency it just wrote.
	if r.cfg != nil && r.cfg.Gateway.Scheduling.AnthropicConfigReconcilerConcurrencyMirrorEnabled {
		r.reconcileConcurrencyMirror(ctx, accounts)
	}

	// Step A: operator (users.id=1) Σ concurrency alignment. Always safe — a
	// no-op when already aligned; covers any drift the mirror introduced.
	r.reconcileOperatorConcurrency(ctx)

	// Step E: edge operator balance floor (edge deployments only).
	if r.cfg != nil && r.cfg.Gateway.Scheduling.AnthropicConfigReconcilerBalanceFloorEnabled {
		r.reconcileBalanceFloor(ctx)
	}

	// Step tier-drift: REPORT ONLY.
	r.reportTierDrift(accounts)

	// Step kiro-priority: HARD-ENFORCE kiro account priority baseline. Fetches its
	// own kiro account list (the list above is anthropic-only). Kiro-scoped; does
	// not touch anthropic priority (owned by the window-rebalance pipeline).
	r.reconcileKiroPriorityBaseline(ctx)

	// Step UA: self-heal the deployment-level Claude Code UA + mimicry manifest
	// settings toward the embedded baseline (in-process `sync-runtime`). Runs once
	// per tick (deployment-scoped, not per-account) so a fresh node auto-acquires
	// the canonical UA without an operator round-trip.
	r.reconcileClaudeCodeMimicry(ctx)
}

// reconcileClaudeCodeMimicry self-heals the Claude Code UA / mimicry settings.
// nil-safe (no-op when the SettingService dep is absent).
func (r *AnthropicConfigReconciler) reconcileClaudeCodeMimicry(ctx context.Context) {
	if r == nil || r.mimicry == nil {
		return
	}
	if changed, err := r.mimicry.EnsureClaudeCodeMimicryBaseline(ctx); err != nil {
		slog.Warn("anthropic config reconciler: claude code mimicry self-heal failed", "err", err)
	} else if changed {
		slog.Info("anthropic config reconciler: claude code mimicry settings self-healed (local deployment only)")
	}
}

// reconcileTierConcurrency value-syncs each tier-bound anthropic OAUTH account's
// concurrency column from its tier row. concurrency stays a persisted column on
// the scheduler hot path; the tier table is the write-source. BulkUpdate already
// enqueues an outbox account_changed event → snapshot rebuild. NEVER writes
// priority (owned by the window-rebalance pipeline).
func (r *AnthropicConfigReconciler) reconcileTierConcurrency(ctx context.Context, accounts []Account) {
	if r.tiers == nil {
		return
	}
	for i := range accounts {
		a := &accounts[i]
		if a.TierID == nil || *a.TierID <= 0 || !a.IsAnthropicOAuthOrSetupToken() {
			continue
		}
		want, ok := r.tiers.ResolveConcurrency(*a.TierID)
		if !ok || want < 0 {
			continue
		}
		if a.Concurrency == want {
			continue
		}
		w := want
		if _, err := r.accounts.BulkUpdate(ctx, []int64{a.ID}, AccountBulkUpdate{Concurrency: &w}); err != nil {
			slog.Warn("anthropic config reconciler: tier concurrency value-sync write failed",
				"account_id", a.ID, "account_name", a.Name, "tier_id", *a.TierID, "want", want, "err", err)
			continue
		}
		slog.Info("anthropic config reconciler: tier concurrency value-synced (local deployment only)",
			"account_id", a.ID, "account_name", a.Name, "tier_id", *a.TierID, "concurrency", want)
	}
}

// reconcileOperatorConcurrency sets users.id=1 concurrency to Σ schedulable
// anthropic concurrency on this DB, then verifies the read-back.
func (r *AnthropicConfigReconciler) reconcileOperatorConcurrency(ctx context.Context) {
	total, err := r.accounts.SumConcurrencyAnthropic(ctx)
	if err != nil {
		slog.Warn("anthropic config reconciler: sum anthropic concurrency failed", "err", err)
		return
	}
	if total < 0 {
		total = 0
	}
	if total > int64(math.MaxInt) {
		slog.Warn("anthropic config reconciler: anthropic concurrency sum overflows int", "sum", total)
		return
	}
	user, err := r.users.GetByID(ctx, AnthropicOperatorConcurrencyUserID)
	if err != nil {
		slog.Warn("anthropic config reconciler: get operator user failed", "err", err)
		return
	}
	if user != nil && int64(user.Concurrency) == total {
		return // already aligned
	}
	if _, err := r.users.BatchSetConcurrency(ctx, []int64{AnthropicOperatorConcurrencyUserID}, int(total)); err != nil {
		slog.Warn("anthropic config reconciler: set operator concurrency failed", "err", err, "want", total)
		return
	}
	// Local verify read-back (keeps the pipeline's determinism discipline).
	if verify, err := r.users.GetByID(ctx, AnthropicOperatorConcurrencyUserID); err == nil && verify != nil && int64(verify.Concurrency) != total {
		slog.Warn("anthropic config reconciler: operator concurrency verify mismatch after write",
			"want", total, "got", verify.Concurrency)
		return
	}
	slog.Info("anthropic config reconciler: operator concurrency self-healed (local deployment only)",
		"user_id", AnthropicOperatorConcurrencyUserID, "concurrency", total)
}

// reconcileStubPoolMode enforces the embedded stub-pool policy on every local
// anthropic api-key account whose base_url matches an internal edge domain. Only
// credentials.pool_mode / pool_mode_retry_count are touched (JSONB merge).
func (r *AnthropicConfigReconciler) reconcileStubPoolMode(ctx context.Context, accounts []Account) {
	doc, re, err := baseline.LoadStubPoolBaseline()
	if err != nil {
		slog.Warn("anthropic config reconciler: load stub-pool baseline failed", "err", err)
		return
	}
	for i := range accounts {
		a := &accounts[i]
		if !r.isMirrorStub(a, re) {
			continue
		}
		wantPool := doc.Policy.PoolModeEnabled
		wantRetry := doc.Policy.PoolModeRetryCount
		if a.IsPoolMode() == wantPool && a.GetPoolModeRetryCount() == wantRetry {
			continue
		}
		updates := AccountBulkUpdate{Credentials: map[string]any{
			"pool_mode":             wantPool,
			"pool_mode_retry_count": wantRetry,
		}}
		if _, err := r.accounts.BulkUpdate(ctx, []int64{a.ID}, updates); err != nil {
			slog.Warn("anthropic config reconciler: stub pool_mode self-heal write failed",
				"account_id", a.ID, "account_name", a.Name, "err", err)
			continue
		}
		slog.Info("anthropic config reconciler: stub pool_mode self-healed (local deployment only)",
			"account_id", a.ID, "account_name", a.Name,
			"pool_mode", wantPool, "pool_mode_retry_count", wantRetry)
	}
}

// reconcileConcurrencyMirror is the surface-C consumer: for each local mirror
// stub, fetch the live capacity from the edge its base_url points at and mirror
// total_concurrency onto stub.concurrency. NEVER writes 0 on any failure.
func (r *AnthropicConfigReconciler) reconcileConcurrencyMirror(ctx context.Context, accounts []Account) {
	_, re, err := baseline.LoadStubPoolBaseline()
	if err != nil {
		slog.Warn("anthropic config reconciler: load stub-pool baseline failed (mirror)", "err", err)
		return
	}
	for i := range accounts {
		a := &accounts[i]
		if !r.isMirrorStub(a, re) {
			continue
		}
		baseURL := strings.TrimSpace(a.GetCredential("base_url"))
		apiKey := strings.TrimSpace(a.GetCredential("api_key"))
		if baseURL == "" || apiKey == "" {
			continue
		}
		// A mirror stub's transport platform is always anthropic-apikey, but the
		// edge pool it represents (its capacity source) may differ — kiro rides the
		// same relay shape. credentials.mirror_platform declares which edge pool to
		// mirror; absent → anthropic (back-compat: every existing stub stays correct).
		platform := mirrorCapacityPlatform(a.GetCredential("mirror_platform"))
		total, ok := r.fetchEdgeCapacity(ctx, baseURL, apiKey, platform)
		if !ok {
			// Hard rule: failure/timeout/5xx/<1 → skip, never write 0.
			continue
		}
		if a.Concurrency == total {
			continue
		}
		want := total
		if _, err := r.accounts.BulkUpdate(ctx, []int64{a.ID}, AccountBulkUpdate{Concurrency: &want}); err != nil {
			slog.Warn("anthropic config reconciler: mirror concurrency write failed",
				"account_id", a.ID, "account_name", a.Name, "want", want, "err", err)
			continue
		}
		slog.Info("anthropic config reconciler: stub concurrency mirrored from edge (local deployment only)",
			"account_id", a.ID, "account_name", a.Name, "base_url", baseURL, "concurrency", want)
	}
}

// mirrorCapacityPlatform normalizes a stub's credentials.mirror_platform into the
// edge capacity platform query value. ONLY empty/whitespace maps to "anthropic"
// (the historical default — every pre-existing mirror stub keeps mirroring the
// anthropic pool). A non-empty value is passed through verbatim (lower/trimmed)
// rather than coerced to a known platform: the edge endpoint is the authoritative
// validator and rejects anything it does not support, so an unknown/typo'd value
// (e.g. "openai", "kir0") makes fetchEdgeCapacity see a 4xx and skip — never write
// 0, never silently mirror the wrong pool. Coercing unknowns to "anthropic" here
// would reintroduce the exact silent-wrong-pool bug this surface exists to kill.
func mirrorCapacityPlatform(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	if p == "" {
		return "anthropic"
	}
	return p
}

// fetchEdgeCapacity GETs {base_url}/api/v1/edge/scheduling-capacity?platform={platform}
// with x-api-key auth. Returns (total, true) only on a 2xx with total_concurrency >= 1.
func (r *AnthropicConfigReconciler) fetchEdgeCapacity(ctx context.Context, baseURL, apiKey, platform string) (int, bool) {
	if r.http == nil {
		return 0, false
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v1/edge/scheduling-capacity?platform=" + url.QueryEscape(platform)
	reqCtx, cancel := context.WithTimeout(ctx, anthropicReconcilerHTTPTO)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		slog.Warn("anthropic config reconciler: build capacity request failed", "base_url", baseURL, "err", err)
		return 0, false
	}
	req.Header.Set("x-api-key", apiKey)
	resp, err := r.http.Do(req)
	if err != nil {
		slog.Warn("anthropic config reconciler: capacity request failed", "base_url", baseURL, "err", err)
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("anthropic config reconciler: capacity request non-2xx", "base_url", baseURL, "status", resp.StatusCode)
		return 0, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		slog.Warn("anthropic config reconciler: read capacity body failed", "base_url", baseURL, "err", err)
		return 0, false
	}
	// The endpoint wraps the payload in the standard {code,message,data} envelope.
	var env struct {
		Data struct {
			TotalConcurrency int `json:"total_concurrency"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		slog.Warn("anthropic config reconciler: decode capacity body failed", "base_url", baseURL, "err", err)
		return 0, false
	}
	if env.Data.TotalConcurrency < 1 {
		slog.Warn("anthropic config reconciler: edge reported capacity < 1; skipping (never writes 0)",
			"base_url", baseURL, "total_concurrency", env.Data.TotalConcurrency)
		return 0, false
	}
	return env.Data.TotalConcurrency, true
}

// reconcileBalanceFloor resets the edge operator (users.id=1) balance to the
// default when it falls below the floor threshold. UpdateUserBalance("set", …)
// also invalidates the billing balance cache.
func (r *AnthropicConfigReconciler) reconcileBalanceFloor(ctx context.Context) {
	if r.balance == nil {
		return
	}
	user, err := r.users.GetByID(ctx, AnthropicOperatorConcurrencyUserID)
	if err != nil {
		slog.Warn("anthropic config reconciler: get operator user for balance floor failed", "err", err)
		return
	}
	if user == nil || user.Balance >= anthropicEdgeBalanceFloorThreshold {
		return
	}
	if _, err := r.balance.UpdateUserBalance(ctx, AnthropicOperatorConcurrencyUserID,
		anthropicEdgeBalanceFloorDefault, "set",
		"anthropic config reconciler: edge operator balance floor self-heal (local deployment only)"); err != nil {
		slog.Warn("anthropic config reconciler: balance floor reset failed", "err", err)
		return
	}
	slog.Info("anthropic config reconciler: edge operator balance floor self-healed (local deployment only)",
		"user_id", AnthropicOperatorConcurrencyUserID,
		"old_balance", user.Balance, "new_balance", anthropicEdgeBalanceFloorDefault)
}

// reportTierDrift is REPORT ONLY. In the reference-table model per-tier numeric
// config is resolved at runtime (overlay) and concurrency is value-synced by Step
// T, so per-field drift no longer exists. The remaining meaningful signal is a
// MIGRATION GAP: an anthropic OAUTH / setup-token account carrying the legacy
// stability_tier label but with no tier_id binding (not tier-resolved at runtime).
func (r *AnthropicConfigReconciler) reportTierDrift(accounts []Account) {
	for i := range accounts {
		a := &accounts[i]
		if !a.IsAnthropicOAuthOrSetupToken() {
			continue
		}
		if a.TierID != nil && *a.TierID > 0 {
			continue // properly bound — runtime resolves per-tier config
		}
		label, _ := a.Extra[AccountTierExtraKey].(string)
		label = strings.TrimSpace(label)
		if label == "" {
			continue // not tier-managed
		}
		slog.Warn("anthropic config reconciler: tier binding gap (REPORT ONLY — has stability_tier label but no tier_id; re-apply via admin UI or run backfill)",
			"account_id", a.ID, "account_name", a.Name, "stability_tier", label)
	}
}

// isMirrorStub reports whether the account is a prod mirror stub: an anthropic
// api-key account whose base_url matches the embedded internal-edge pattern.
func (r *AnthropicConfigReconciler) isMirrorStub(a *Account, re interface{ MatchString(string) bool }) bool {
	if a == nil || re == nil {
		return false
	}
	if a.Platform != PlatformAnthropic || a.Type != AccountTypeAPIKey {
		return false
	}
	baseURL := strings.TrimSpace(a.GetCredential("base_url"))
	return baseURL != "" && re.MatchString(baseURL)
}
