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

// AntigravityConfigReconciler runs a single ticker that, each tick (and once
// immediately on start), acquires a redis leader lock (best-effort; degrades to
// lockless on no redis) and enforces gemini-only model_mapping on the local DB's
// antigravity accounts.
type AntigravityConfigReconciler struct {
	accounts   antigravityReconcilerAccountStore
	cfg        *config.Config
	redis      *redis.Client
	instanceID string

	stopCh   chan struct{}
	stopOnce sync.Once
	startOne sync.Once
	wg       sync.WaitGroup

	warnNoRedisOnce sync.Once
}

// NewAntigravityConfigReconciler constructs the reconciler. A nil accounts store
// produces a no-op on Start, keeping wire wiring safe for minimal test deps.
func NewAntigravityConfigReconciler(
	accounts antigravityReconcilerAccountStore,
	cfg *config.Config,
	redisClient *redis.Client,
) *AntigravityConfigReconciler {
	return &AntigravityConfigReconciler{
		accounts:   accounts,
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

// antigravityCanServeExcluded reports whether an antigravity account can still
// serve a claude-* or gpt-oss-* model and therefore violates the gemini-only
// policy. Two complementary checks (mirroring the post-rollout
// check-antigravity-account-config.py invariant so the reconciler heals exactly
// what the check flags):
//   - scan the resolved mapping keys for a claude-* / gpt-oss-* prefix — catches
//     ANY excluded id (e.g. claude-opus-4-8), not just the probe ids, and catches
//     the empty-map case (which resolves to DefaultAntigravityModelMapping);
//   - probe the representative ids — catches a catch-all wildcard (e.g. "*") that
//     would match claude/gpt-oss without carrying a literal claude-* key.
func antigravityCanServeExcluded(a *Account) bool {
	for k := range a.GetModelMapping() {
		if strings.HasPrefix(k, "claude-") || strings.HasPrefix(k, "gpt-oss-") {
			return true
		}
	}
	return a.IsModelSupported(antigravityClaudeProbeModel) || a.IsModelSupported(antigravityGptOssProbeModel)
}

// runOnce enforces gemini-only model_mapping on every antigravity account in the
// LOCAL DB. An account that can still serve claude/gpt-oss (an empty mapping
// falls back to DefaultAntigravityModelMapping, which includes both) has its
// credentials.model_mapping rewritten to GeminiOnlyAntigravityModelMapping via
// BulkUpdate (whose JSONB shallow-merge replaces the whole model_mapping
// sub-object — dropping claude/gpt-oss keys — and enqueues a scheduler_outbox
// event so the change takes effect). Tests call it directly.
func (r *AntigravityConfigReconciler) runOnce(ctx context.Context) {
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
