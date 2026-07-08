package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

const (
	accountModelMappingReconcilerLockTTL = 4 * time.Minute
	accountModelMappingReconcilerRunTO   = 60 * time.Second
)

type accountModelMappingAccountStore interface {
	ListByPlatform(ctx context.Context, platform string) ([]Account, error)
	BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error)
}

type accountModelMappingGroupStore interface {
	ListActiveByPlatform(ctx context.Context, platform string) ([]Group, error)
	Update(ctx context.Context, group *Group) error
}

type accountModelMappingSettingReader interface {
	GetRawSettingValue(ctx context.Context, key string) (string, bool)
}

type accountModelMappingLeaderLock interface {
	TryAcquire(ctx context.Context, holder string, ttl time.Duration) (func(), bool)
}

// AccountModelMappingReconciler keeps every active account on an explicit
// credentials.model_mapping derived from TokenKey's servable+priced+displayable
// SSOT. Empty native mappings no longer act as an operational policy surface:
// they are drift that this reconciler rewrites to the canonical whitelist.
type AccountModelMappingReconciler struct {
	accounts     accountModelMappingAccountStore
	groups       accountModelMappingGroupStore
	settings     accountModelMappingSettingReader
	pricing      *PricingCatalogService
	availability MePricingAvailability
	cfg          *config.Config
	lock         accountModelMappingLeaderLock
	pubsub       SettingPubSub
	instanceID   string

	stopCh       chan struct{}
	stopOnce     sync.Once
	startOne     sync.Once
	wg           sync.WaitGroup
	pubsubCancel context.CancelFunc

	warnNoRedisOnce sync.Once
}

func NewAccountModelMappingReconciler(
	accounts accountModelMappingAccountStore,
	groups accountModelMappingGroupStore,
	settings accountModelMappingSettingReader,
	pricing *PricingCatalogService,
	availability *PricingAvailabilityService,
	cfg *config.Config,
	lock accountModelMappingLeaderLock,
	pubsub SettingPubSub,
) *AccountModelMappingReconciler {
	var avail MePricingAvailability
	if availability != nil {
		avail = availability
	}
	return &AccountModelMappingReconciler{
		accounts:     accounts,
		groups:       groups,
		settings:     settings,
		pricing:      pricing,
		availability: avail,
		cfg:          cfg,
		lock:         lock,
		pubsub:       pubsub,
		instanceID:   uuid.NewString(),
		stopCh:       make(chan struct{}),
	}
}

func (r *AccountModelMappingReconciler) tickInterval() time.Duration {
	if r == nil || r.cfg == nil {
		return 0
	}
	sec := r.cfg.Gateway.Scheduling.AccountModelMappingReconcilerIntervalSeconds
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func (r *AccountModelMappingReconciler) Start() {
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
		if r.pubsub != nil {
			pubsubCtx, cancel := context.WithCancel(context.Background())
			r.pubsubCancel = cancel
			r.pubsub.Subscribe(pubsubCtx, func() {
				go r.runOnceLocked()
			})
		}
	})
}

func (r *AccountModelMappingReconciler) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.pubsubCancel != nil {
			r.pubsubCancel()
		}
		close(r.stopCh)
	})
	r.wg.Wait()
}

func (r *AccountModelMappingReconciler) runOnceLocked() {
	release, ok := r.tryAcquireLock()
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountModelMappingReconcilerRunTO)
	defer cancel()
	r.runOnce(ctx)
}

func (r *AccountModelMappingReconciler) tryAcquireLock() (func(), bool) {
	if r.lock == nil {
		r.warnNoRedisOnce.Do(func() {
			slog.Warn("account model_mapping reconciler running without distributed lock (no redis)")
		})
		return nil, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.lock.TryAcquire(ctx, r.instanceID, accountModelMappingReconcilerLockTTL)
}

func (r *AccountModelMappingReconciler) runOnce(ctx context.Context) {
	if r == nil {
		return
	}
	runtime := r.runtimeOverride(ctx)
	r.reconcileAccounts(ctx, runtime)
	r.reconcileAntigravityGroupScopes(ctx)
}

func (r *AccountModelMappingReconciler) runtimeOverride(ctx context.Context) *accountModelMappingRuntime {
	if r == nil || r.settings == nil {
		return nil
	}
	raw, ok := r.settings.GetRawSettingValue(ctx, SettingKeyTKAccountModelMappingRuntime)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	runtime, err := parseAccountModelMappingRuntime(raw)
	if err != nil {
		slog.Warn("account model_mapping reconciler: invalid runtime SSOT blob; using compiled floor", "err", err)
		return nil
	}
	return runtime
}

func (r *AccountModelMappingReconciler) reconcileAccounts(ctx context.Context, runtime *accountModelMappingRuntime) {
	if r == nil || r.accounts == nil {
		return
	}
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformNewAPI, PlatformKiro, PlatformGrok} {
		accounts, err := r.accounts.ListByPlatform(ctx, platform)
		if err != nil {
			slog.Warn("account model_mapping reconciler: list accounts failed", "platform", platform, "err", err)
			continue
		}
		r.reconcileAccountBatch(ctx, accounts, runtime)
	}
}

func (r *AccountModelMappingReconciler) reconcileAccountBatch(ctx context.Context, accounts []Account, runtime *accountModelMappingRuntime) {
	if len(accounts) == 0 {
		return
	}
	idsBySig := make(map[string][]int64)
	mappingBySig := make(map[string]map[string]string)
	for i := range accounts {
		account := &accounts[i]
		want, ok := accountModelMappingForAccount(ctx, account, r.pricing, r.availability, runtime)
		if !ok || len(want) == 0 {
			continue
		}
		if modelMappingsEqual(accountRawModelMapping(account), want) {
			continue
		}
		sig := modelMappingSignatureString(want)
		idsBySig[sig] = append(idsBySig[sig], account.ID)
		mappingBySig[sig] = want
	}
	for sig, ids := range idsBySig {
		mapping := mappingBySig[sig]
		if len(ids) == 0 || len(mapping) == 0 {
			continue
		}
		if _, err := r.accounts.BulkUpdate(ctx, ids, AccountBulkUpdate{
			Credentials: map[string]any{"model_mapping": modelMappingToAny(mapping)},
		}); err != nil {
			slog.Warn("account model_mapping reconciler: bulk update failed", "count", len(ids), "err", err)
			continue
		}
		slog.Info("account model_mapping reconciler: enforced explicit model_mapping", "accounts", len(ids), "models", len(mapping))
	}
}

func (r *AccountModelMappingReconciler) reconcileAntigravityGroupScopes(ctx context.Context) {
	if r == nil || r.groups == nil {
		return
	}
	groups, err := r.groups.ListActiveByPlatform(ctx, PlatformAntigravity)
	if err != nil {
		slog.Warn("account model_mapping reconciler: list antigravity groups failed", "err", err)
		return
	}
	enforced := 0
	for i := range groups {
		g := &groups[i]
		if stringSlicesSameSet(g.SupportedModelScopes, canonicalAntigravityModelScopes) {
			continue
		}
		g.SupportedModelScopes = append([]string(nil), canonicalAntigravityModelScopes...)
		if err := r.groups.Update(ctx, g); err != nil {
			slog.Warn("account model_mapping reconciler: update antigravity group scopes failed", "group", g.ID, "err", err)
			continue
		}
		enforced++
	}
	if enforced > 0 {
		slog.Info("account model_mapping reconciler: enforced antigravity group scopes", "groups", enforced)
	}
}

var canonicalAntigravityModelScopes = []string{"claude", "gemini_text", "gemini_image"}

func stringSlicesSameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(got))
	for _, s := range got {
		seen[strings.TrimSpace(s)] = true
	}
	for _, s := range want {
		if !seen[s] {
			return false
		}
	}
	return true
}
