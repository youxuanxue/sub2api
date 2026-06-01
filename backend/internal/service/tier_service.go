package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/baseline"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TierRepository 定义 tier（anthropic OAuth 稳定性档位）的数据访问接口。
type TierRepository interface {
	List(ctx context.Context) ([]*model.Tier, error)
	GetByID(ctx context.Context, id int64) (*model.Tier, error)
	GetByName(ctx context.Context, name string) (*model.Tier, error)
	Create(ctx context.Context, t *model.Tier) (*model.Tier, error)
	Update(ctx context.Context, t *model.Tier) (*model.Tier, error)
	Delete(ctx context.Context, id int64) error
	UpsertByName(ctx context.Context, t *model.Tier) (*model.Tier, error)
}

// TierCache 定义 tier 的缓存接口（Redis pub/sub，与 TLS 同构）。
type TierCache interface {
	Get(ctx context.Context) ([]*model.Tier, bool)
	Set(ctx context.Context, tiers []*model.Tier) error
	Invalidate(ctx context.Context) error
	NotifyUpdate(ctx context.Context) error
	SubscribeUpdates(ctx context.Context, handler func())
}

// TierExtraResolver 在账号加载边界把 tier 的 per-tier 配置 overlay 进内存 Extra。
// *TierService 实现它；accountRepository 通过此接口注入（避免 repo 依赖具体 service）。
type TierExtraResolver interface {
	ApplyTierExtra(account *Account)
	// TierManagedExtraStripped 返回剥离了 tier-overlay 键的 extra 副本（写路径用），
	// 防止内存 overlay 泄漏进 DB。非 tier-managed 账号原样返回 account.Extra。
	TierManagedExtraStripped(account *Account) map[string]any
}

// TierService 管理 anthropic OAuth 稳定性档位（CRUD + Redis pub/sub 缓存 + 运行时
// 解析）。和 TLSFingerprintProfileService 同构。账号通过 tier_id 引用，改一行 tier
// 经 pub/sub 秒级 fan-out（账号零写）。git baseline JSON 是权威源，TierService 启动
// 时 ensureSeededFromBaseline 把 embed baseline 重断言进本表（自愈漂移）。
type TierService struct {
	repo  TierRepository
	cache TierCache

	localMu sync.RWMutex
	byID    map[int64]*model.Tier
	byName  map[string]*model.Tier
}

// NewTierService 创建 tier 服务：从 baseline 重断言 seed → 从 DB 载入本地缓存 →
// 订阅 pub/sub 刷新。
func NewTierService(repo TierRepository, cache TierCache) *TierService {
	svc := &TierService{
		repo:   repo,
		cache:  cache,
		byID:   make(map[int64]*model.Tier),
		byName: make(map[string]*model.Tier),
	}

	ctx := context.Background()
	if err := svc.ensureSeededFromBaseline(ctx); err != nil {
		logger.LegacyPrintf("service.tier", "[TierService] ensureSeededFromBaseline failed on startup: %v", err)
	}
	if err := svc.reloadFromDB(ctx); err != nil {
		logger.LegacyPrintf("service.tier", "[TierService] Failed to load tiers from DB on startup: %v", err)
		if fallbackErr := svc.refreshLocalCache(ctx); fallbackErr != nil {
			logger.LegacyPrintf("service.tier", "[TierService] Failed to load tiers from cache fallback on startup: %v", fallbackErr)
		}
	}

	if cache != nil {
		cache.SubscribeUpdates(ctx, func() {
			if err := svc.refreshLocalCache(context.Background()); err != nil {
				logger.LegacyPrintf("service.tier", "[TierService] Failed to refresh cache on notification: %v", err)
			}
		})
	}

	return svc
}

// --- CRUD（写后 invalidateAndNotify，pub/sub fan-out） ---

func (s *TierService) List(ctx context.Context) ([]*model.Tier, error) {
	return s.repo.List(ctx)
}

func (s *TierService) GetByID(ctx context.Context, id int64) (*model.Tier, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *TierService) GetByName(ctx context.Context, name string) (*model.Tier, error) {
	return s.repo.GetByName(ctx, name)
}

func (s *TierService) Create(ctx context.Context, t *model.Tier) (*model.Tier, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	created, err := s.repo.Create(ctx, t)
	if err != nil {
		return nil, err
	}
	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)
	return created, nil
}

func (s *TierService) Update(ctx context.Context, t *model.Tier) (*model.Tier, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	updated, err := s.repo.Update(ctx, t)
	if err != nil {
		return nil, err
	}
	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)
	return updated, nil
}

func (s *TierService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)
	return nil
}

// --- 运行时解析 ---

// ApplyTierExtra 把账号绑定 tier 的 per-tier 数值字段 overlay 进内存 Extra（不持久化）。
// 仅对有 tier_id 的 anthropic oauth 账号生效；其它账号原样返回（走 getter 既有 fallback）。
func (s *TierService) ApplyTierExtra(account *Account) {
	if s == nil || account == nil || account.TierID == nil || *account.TierID <= 0 {
		return
	}
	if !account.IsAnthropicOAuthOrSetupToken() {
		return
	}
	t := s.lookupByID(*account.TierID)
	if t == nil {
		return
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any, 8)
	}
	t.OverlayExtra(account.Extra)
}

// TierManagedExtraStripped 返回剥离 tier-overlay 键后的 extra 副本（写路径在
// repo.Update 调用），保证内存 overlay 不持久化到账号。非 tier-managed 账号
// （无 tier_id / 非 anthropic-oauth/setup-token）原样返回。
func (s *TierService) TierManagedExtraStripped(account *Account) map[string]any {
	if account == nil {
		return nil
	}
	if account.TierID == nil || *account.TierID <= 0 || !account.IsAnthropicOAuthOrSetupToken() {
		return account.Extra
	}
	if account.Extra == nil {
		return nil
	}
	out := make(map[string]any, len(account.Extra))
	for k, v := range account.Extra {
		if model.IsTierManagedExtraKey(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// ResolveConcurrency 返回 tier 的 concurrency（reconciler 值同步用）。
func (s *TierService) ResolveConcurrency(tierID int64) (int, bool) {
	t := s.lookupByID(tierID)
	if t == nil {
		return 0, false
	}
	return t.Concurrency, true
}

func (s *TierService) lookupByID(id int64) *model.Tier {
	s.localMu.RLock()
	t := s.byID[id]
	s.localMu.RUnlock()
	return t
}

// --- seed + 缓存管理 ---

// ensureSeededFromBaseline 把 embed baseline 的 l1..l5 effective 配置 upsert 进
// tiers 表（每次启动重断言，git→DB 自愈）。tls_profile_id 不在此设置（由 apply /
// 流水线维护）。
func (s *TierService) ensureSeededFromBaseline(ctx context.Context) error {
	order, err := baseline.TierOrder()
	if err != nil {
		return err
	}
	for _, name := range order {
		eff, err := baseline.EffectiveBaselineForTier(name)
		if err != nil {
			return err
		}
		t := tierFromEffectiveBaseline(eff)
		if _, err := s.repo.UpsertByName(ctx, t); err != nil {
			logger.LegacyPrintf("service.tier", "[TierService] seed upsert tier %q failed: %v", name, err)
			return err
		}
	}
	return nil
}

func (s *TierService) refreshLocalCache(ctx context.Context) error {
	if s.cache != nil {
		if tiers, ok := s.cache.Get(ctx); ok {
			s.setLocalCache(tiers)
			return nil
		}
	}
	return s.reloadFromDB(ctx)
}

func (s *TierService) reloadFromDB(ctx context.Context) error {
	tiers, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	if s.cache != nil {
		if err := s.cache.Set(ctx, tiers); err != nil {
			logger.LegacyPrintf("service.tier", "[TierService] Failed to set cache: %v", err)
		}
	}
	s.setLocalCache(tiers)
	return nil
}

func (s *TierService) setLocalCache(tiers []*model.Tier) {
	byID := make(map[int64]*model.Tier, len(tiers))
	byName := make(map[string]*model.Tier, len(tiers))
	for _, t := range tiers {
		if t == nil {
			continue
		}
		byID[t.ID] = t
		byName[t.Name] = t
	}
	s.localMu.Lock()
	s.byID = byID
	s.byName = byName
	s.localMu.Unlock()
}

func (s *TierService) newCacheRefreshContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

func (s *TierService) invalidateAndNotify(ctx context.Context) {
	if s.cache != nil {
		if err := s.cache.Invalidate(ctx); err != nil {
			logger.LegacyPrintf("service.tier", "[TierService] Failed to invalidate cache: %v", err)
		}
	}
	if err := s.reloadFromDB(ctx); err != nil {
		logger.LegacyPrintf("service.tier", "[TierService] Failed to refresh local cache: %v", err)
		s.localMu.Lock()
		s.byID = make(map[int64]*model.Tier)
		s.byName = make(map[string]*model.Tier)
		s.localMu.Unlock()
	}
	if s.cache != nil {
		if err := s.cache.NotifyUpdate(ctx); err != nil {
			logger.LegacyPrintf("service.tier", "[TierService] Failed to notify cache update: %v", err)
		}
	}
}

// tierFromEffectiveBaseline 把 baseline.EffectiveTierBaseline 转成 model.Tier。
// Extra 里的数值是 JSON 解出的（float64），用 baseline 的解析口径取整/取浮点。
func tierFromEffectiveBaseline(eff *baseline.EffectiveTierBaseline) *model.Tier {
	t := &model.Tier{
		Name:                      eff.Tier,
		Concurrency:               eff.Concurrency,
		Priority:                  eff.Priority,
		RateMultiplier:            eff.RateMultiplier,
		BaseRPM:                   extraInt(eff.Extra, "base_rpm"),
		MaxSessions:               extraInt(eff.Extra, "max_sessions"),
		RPMStickyBuffer:           extraInt(eff.Extra, "rpm_sticky_buffer"),
		SessionIdleTimeoutMinutes: extraInt(eff.Extra, "session_idle_timeout_minutes"),
		WindowCostLimit:           extraFloat(eff.Extra, "window_cost_limit"),
		WindowCostStickyReserve:   extraFloat(eff.Extra, "window_cost_sticky_reserve"),
		CacheTTLOverrideEnabled:   extraBool(eff.Extra, "cache_ttl_override_enabled"),
	}
	if v, ok := eff.Extra["cache_ttl_override_target"].(string); ok && v != "" {
		t.CacheTTLOverrideTarget = &v
	}
	if name := eff.TLSProfileName; name != "" {
		t.TLSProfileName = &name
	}
	return t
}

func extraInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func extraFloat(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func extraBool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}
