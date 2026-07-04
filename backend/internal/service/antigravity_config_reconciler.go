package service

// AntigravityConfigReconciler is the per-node, in-process self-healer that
// enforces the operator policy "Antigravity serves gemini only" on every
// antigravity account in THIS deployment's database — no manual per-account
// model_mapping edit required.
//
// Why it exists: an antigravity account with an empty credentials.model_mapping
// falls back to domain.DefaultAntigravityModelMapping, which still includes
// claude-* and gpt-oss-* (kept there as a code-level capability per CLAUDE.md
// §5.x keep-don't-strip). To route claude off antigravity (to the anthropic
// pool) and drop gpt-oss, each account must carry a gemini-only custom mapping.
// This reconciler applies that mapping automatically on deploy and keeps it
// applied (current accounts, future-created accounts, and any drift).
//
// Boundaries (mirrors AnthropicConfigReconciler):
//   - Writes ONLY this deployment's own DB; never fleet-wide.
//   - Skip-if-aligned: only accounts that can still serve claude/gpt-oss are
//     rewritten; already-gemini-only accounts are left untouched (idempotent,
//     no write thrash — the canonical map has no wildcards, so the probe is
//     stable across ticks).
//   - It does NOT touch domain.DefaultAntigravityModelMapping — claude stays a
//     code-level capability; only per-account data is reconciled.
//
// Divergence from AnthropicConfigReconciler: this one runs an immediate pass at
// goroutine start (before the ticker loop) so the gemini-only policy takes
// effect promptly on boot / post-deploy, then on every tick.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	antigravityReconcilerLockKey = "antigravity:config:reconciler:leader"
	antigravityReconcilerLockTTL = 4 * time.Minute
	antigravityReconcilerRunTO   = 60 * time.Second

	// Representative ids that exist in DefaultAntigravityModelMapping. If an
	// account's resolved mapping supports either, it can still serve claude /
	// gpt-oss and therefore needs reconciling to gemini-only.
	antigravityClaudeProbeModel = "claude-sonnet-4-6"
	antigravityGptOssProbeModel = "gpt-oss-120b-medium"
)

// antigravityReconcilerLockRelease is the compare-and-delete unlock script — only
// the instance that holds the lock may delete it (mirrors the anthropic one).
var antigravityReconcilerLockRelease = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// antigravityReconcilerAccountStore is the narrow account dependency.
// *accountRepository (AccountRepository) satisfies it; the small interface keeps
// runOnce unit-testable without a full repository stub.
type antigravityReconcilerAccountStore interface {
	ListByPlatform(ctx context.Context, platform string) ([]Account, error)
	BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error)
}

// antigravityReconcilerGroupStore is the narrow group dependency. GroupRepository
// satisfies it (both methods already exist on that interface), so no new repo
// surface / stub churn is needed. Group scopes have no token-like sibling field,
// so the reconciler reuses the existing full Update (read-modify-write) rather
// than a partial column write — and only fires on drift (idempotent), so the
// clobber window is a freshly-created / just-edited group, negligible in practice.
type antigravityReconcilerGroupStore interface {
	ListActiveByPlatform(ctx context.Context, platform string) ([]Group, error)
	Update(ctx context.Context, group *Group) error
}

// AntigravityConfigReconciler runs a single ticker that, each tick (and once
// immediately on start), acquires a redis leader lock (best-effort; degrades to
// lockless on no redis) and enforces the gemini-only operator policy on the local
// DB: gemini-only model_mapping on antigravity accounts AND gemini-only
// supported_model_scopes on antigravity groups (so /antigravity/v1/models + the
// API key usage guide hide claude for new / drifted groups too).
type AntigravityConfigReconciler struct {
	accounts   antigravityReconcilerAccountStore
	groups     antigravityReconcilerGroupStore
	cfg        *config.Config
	redis      *redis.Client
	instanceID string

	stopCh   chan struct{}
	stopOnce sync.Once
	startOne sync.Once
	wg       sync.WaitGroup

	warnNoRedisOnce sync.Once
}

// NewAntigravityConfigReconciler constructs the reconciler. A nil accounts/groups
// store no-ops that half, keeping wire wiring safe for minimal test deps.
func NewAntigravityConfigReconciler(
	accounts antigravityReconcilerAccountStore,
	groups antigravityReconcilerGroupStore,
	cfg *config.Config,
	redisClient *redis.Client,
) *AntigravityConfigReconciler {
	return &AntigravityConfigReconciler{
		accounts:   accounts,
		groups:     groups,
		cfg:        cfg,
		redis:      redisClient,
		instanceID: uuid.NewString(),
		stopCh:     make(chan struct{}),
	}
}

// tickInterval mirrors the anthropic reconciler: <=0 disables the goroutine;
// viper.SetDefault supplies the non-zero default.
func (r *AntigravityConfigReconciler) tickInterval() time.Duration {
	if r == nil || r.cfg == nil {
		return 0
	}
	sec := r.cfg.Gateway.Scheduling.AntigravityConfigReconcilerIntervalSeconds
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// Start launches the reconciler goroutine. Safe to call once; a nil store or
// zero interval no-ops.
func (r *AntigravityConfigReconciler) Start() {
	if r == nil || r.accounts == nil {
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
			// Immediate first pass on boot so the gemini-only policy takes effect
			// promptly post-deploy (divergence from the anthropic reconciler,
			// which only acts on the first tick).
			r.runOnceLocked()
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

// Stop signals the goroutine to exit and waits. Safe to call once; nil-safe.
func (r *AntigravityConfigReconciler) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	r.wg.Wait()
}

// runOnceLocked acquires the redis leader lock (if redis is configured) and runs
// runOnce. Multiple replicas thus self-heal at most once per tick for a given DB;
// single-node / no-redis runs lockless.
func (r *AntigravityConfigReconciler) runOnceLocked() {
	release, ok := r.tryAcquireLock()
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), antigravityReconcilerRunTO)
	defer cancel()
	r.runOnce(ctx)
}

func (r *AntigravityConfigReconciler) tryAcquireLock() (func(), bool) {
	if r.redis == nil {
		r.warnNoRedisOnce.Do(func() {
			slog.Warn("antigravity config reconciler running without distributed lock (no redis)")
		})
		return nil, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acquired, err := r.redis.SetNX(ctx, antigravityReconcilerLockKey, r.instanceID, antigravityReconcilerLockTTL).Result()
	if err != nil {
		// Fail-closed: skip this cycle rather than risk concurrent self-heal.
		slog.Warn("antigravity config reconciler leader lock SetNX failed; skipping cycle", "err", err)
		return nil, false
	}
	if !acquired {
		return nil, false
	}
	return func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_, _ = antigravityReconcilerLockRelease.Run(releaseCtx, r.redis, []string{antigravityReconcilerLockKey}, r.instanceID).Result()
	}, true
}

// antigravityCanServeExcluded reports whether an antigravity account carries any
// mapping drift the canonical gemini-only account map must heal: excluded
// claude/gpt-oss families, PR #921 structural-dead Antigravity aliases, or
// unpriced Antigravity models that would bill at $0. Two
// complementary checks (mirroring the post-rollout check-antigravity-account-config.py
// invariant so the reconciler heals exactly what the check flags):
//   - scan the persisted model_mapping keys for a claude-* / gpt-oss-* prefix or
//     structural-dead/unpriced key — catches ANY excluded id (e.g.
//     claude-opus-4-8), stale Antigravity aliases, and $0-risk models, not just
//     the probe ids, and catches the empty-map case (which resolves to
//     DefaultAntigravityModelMapping), without treating synthetic safety-net
//     Gemini aliases as persisted drift;
//   - probe the representative ids — catches a catch-all wildcard (e.g. "*") that
//     would match claude/gpt-oss without carrying a literal claude-* key.
func antigravityCanServeExcluded(a *Account) bool {
	if a == nil {
		return false
	}
	rawMapping, _ := a.Credentials["model_mapping"].(map[string]any)
	if len(rawMapping) == 0 {
		// Empty / malformed custom mappings fall back to DefaultAntigravityModelMapping,
		// which still contains excluded families and compatibility aliases.
		return true
	}
	for k := range rawMapping {
		if strings.HasPrefix(k, "claude-") || strings.HasPrefix(k, "gpt-oss-") {
			return true
		}
		if domain.IsAntigravityStructuralDeadModelMappingKey(k) || domain.IsAntigravityUnpricedModelMappingKey(k) {
			return true
		}
	}
	return a.IsModelSupported(antigravityClaudeProbeModel) || a.IsModelSupported(antigravityGptOssProbeModel)
}

// runOnce enforces gemini-only model_mapping on every antigravity account in the
// LOCAL DB. An account that can still serve claude/gpt-oss or carries stale
// structural-dead aliases / unpriced keys (an empty mapping falls back to
// DefaultAntigravityModelMapping, which includes excluded families and
// compatibility aliases) has its
// credentials.model_mapping rewritten to GeminiOnlyAntigravityModelMapping via
// BulkUpdate (whose JSONB shallow-merge replaces the whole model_mapping sub-object
// — dropping excluded/stale keys — and enqueues a scheduler_outbox event so the
// change takes effect). Tests call it directly.
func (r *AntigravityConfigReconciler) runOnce(ctx context.Context) {
	if r == nil {
		return
	}
	r.reconcileAccounts(ctx)
	r.reconcileGroups(ctx)
}

// reconcileAccounts enforces gemini-only model_mapping on every antigravity account.
func (r *AntigravityConfigReconciler) reconcileAccounts(ctx context.Context) {
	if r == nil || r.accounts == nil {
		return
	}
	accounts, err := r.accounts.ListByPlatform(ctx, PlatformAntigravity)
	if err != nil {
		slog.Warn("antigravity config reconciler: list accounts failed", "err", err)
		return
	}
	var drifted []int64
	for i := range accounts {
		a := &accounts[i]
		if antigravityCanServeExcluded(a) {
			drifted = append(drifted, a.ID)
		}
	}
	if len(drifted) == 0 {
		return
	}
	geminiOnly := make(map[string]any, len(domain.GeminiOnlyAntigravityModelMapping))
	for k, v := range domain.GeminiOnlyAntigravityModelMapping {
		geminiOnly[k] = v
	}
	if _, err := r.accounts.BulkUpdate(ctx, drifted, AccountBulkUpdate{
		Credentials: map[string]any{"model_mapping": geminiOnly},
	}); err != nil {
		slog.Warn("antigravity config reconciler: bulk update failed", "err", err, "count", len(drifted))
		return
	}
	slog.Info("antigravity config reconciler: enforced gemini-only model_mapping", "accounts", len(drifted))
}

// reconcileGroups enforces gemini-only supported_model_scopes on every antigravity
// group, so /antigravity/v1/models and the API key usage guide hide claude for
// newly-created or operator-drifted groups too — symmetric with the per-account
// model_mapping enforcement, and the request-path consumer of supported_model_scopes
// (gateway_handler AntigravityModels filter + UseKeyModal claude-flavor gate).
func (r *AntigravityConfigReconciler) reconcileGroups(ctx context.Context) {
	if r == nil || r.groups == nil {
		return
	}
	groups, err := r.groups.ListActiveByPlatform(ctx, PlatformAntigravity)
	if err != nil {
		slog.Warn("antigravity config reconciler: list groups failed", "err", err)
		return
	}
	enforced := 0
	for i := range groups {
		g := &groups[i]
		if !antigravityGroupScopesNeedGeminiOnly(g.SupportedModelScopes) {
			continue
		}
		g.SupportedModelScopes = append([]string(nil), domain.GeminiOnlyAntigravityModelScopes...)
		if err := r.groups.Update(ctx, g); err != nil {
			slog.Warn("antigravity config reconciler: update group scopes failed", "err", err, "group", g.ID)
			continue
		}
		enforced++
	}
	if enforced > 0 {
		slog.Info("antigravity config reconciler: enforced gemini-only group scopes", "groups", enforced)
	}
}

// antigravityGroupScopesNeedGeminiOnly reports whether a group's
// supported_model_scopes diverges from the canonical gemini-only set
// (domain.GeminiOnlyAntigravityModelScopes). Empty/nil (= unrestricted → advertises
// claude), claude present, or any non-canonical set (extra/missing/duplicate) all
// count as drift. Order-independent.
func antigravityGroupScopesNeedGeminiOnly(scopes []string) bool {
	want := domain.GeminiOnlyAntigravityModelScopes
	if len(scopes) != len(want) {
		return true
	}
	got := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		got[strings.TrimSpace(s)] = true
	}
	for _, w := range want {
		if !got[w] {
			return true
		}
	}
	return false
}
