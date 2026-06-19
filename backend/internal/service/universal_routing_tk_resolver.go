package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/sync/singleflight"
)

// UniversalRoutingResolver 解析“全能 Key”每个请求应落到的后端组。
//
// 流程（见 docs/approved/universal-key-routing.md）：
//  1. 取 key 主人的权限跨度（GetAvailableGroups，带短 TTL 缓存，热路径命中 0 次 DB）；
//  2. 按入口端点形状（+ /antigravity 的 forcedPlatform）得到候选平台集合；
//  3. 跨度 ∩ 候选平台 → 候选后端组；用模型平台提示偏向、再按确定规则挑一个
//     （持订阅优先 → group.sort_order → id）。
//
// 解析成功后，调用方（middleware.MaybeResolveUniversal）把请求“伪装”成绑定该后端组的
// 普通 key（替换 apiKey.Group/GroupID），下游调度/计费/转发零改动。
type UniversalRoutingResolver struct {
	lister availableGroupsLister
	ttl    time.Duration

	sf    singleflight.Group
	mu    sync.RWMutex
	cache map[int64]*spanCacheEntry
}

// availableGroupsLister 由 *APIKeyService 满足，给出某用户当前有权绑定的全部分组
// （公开组 + 专属授权 + 生效订阅）。新授权天然反映在结果里（受 TTL 限制最多滞后一个 TTL）。
type availableGroupsLister interface {
	GetAvailableGroups(ctx context.Context, userID int64) ([]Group, error)
}

type spanCacheEntry struct {
	groups  []Group
	expires time.Time
}

// ErrUniversalNoEntitledGroup 表示该全能 key 主人没有任何被授权的后端组能服务此端点/平台。
// middleware 据此按入口协议（anthropic/google/openai）写出对应形状的错误。
var ErrUniversalNoEntitledGroup = errors.New("universal key: no entitled backing group for this request")

const (
	defaultUniversalSpanTTL = 30 * time.Second
	// 缓存超过该条目数时,写路径清扫已过期条目(界定内存,避免无界增长)。
	spanCacheSweepThreshold = 1024
	// TTL 抖动桶数(秒);把同批用户的过期时刻摊开到一个窗口内。
	spanCacheJitterBuckets = 17
)

// NewUniversalRoutingResolver 构造解析器（含进程内权限跨度缓存）。lister 一般是 *APIKeyService。
func NewUniversalRoutingResolver(lister availableGroupsLister) *UniversalRoutingResolver {
	return &UniversalRoutingResolver{
		lister: lister,
		ttl:    defaultUniversalSpanTTL,
		cache:  make(map[int64]*spanCacheEntry),
	}
}

// Resolve 返回该请求应落到的后端组。shape 为入口端点形状，model 为请求模型名（可空，
// 仅作平台偏好提示），forcedPlatform 非空时（如 /antigravity）只在该平台内解析。
func (r *UniversalRoutingResolver) Resolve(ctx context.Context, apiKey *APIKey, shape UniversalShape, model, forcedPlatform string) (*Group, error) {
	if r == nil || apiKey == nil {
		return nil, ErrUniversalNoEntitledGroup
	}
	span, err := r.span(ctx, apiKey.UserID)
	if err != nil {
		return nil, err
	}

	candidates := universalCandidatePlatforms(shape, forcedPlatform, spanHasMessagesDispatch(span))
	if len(candidates) == 0 {
		return nil, ErrUniversalNoEntitledGroup
	}
	candidateSet := make(map[string]struct{}, len(candidates))
	for _, p := range candidates {
		candidateSet[p] = struct{}{}
	}

	eligible := make([]Group, 0, len(span))
	for i := range span {
		g := span[i]
		if !g.IsActive() {
			continue
		}
		if _, ok := candidateSet[g.Platform]; ok {
			eligible = append(eligible, g)
		}
	}
	if len(eligible) == 0 {
		return nil, ErrUniversalNoEntitledGroup
	}

	// 模型平台提示：若提示命中且跨度内有该平台的 eligible 组，仅在这些组里挑（偏向，
	// 非硬过滤——提示未命中则退回全体 eligible）。这样 openai-compat 形状下能把
	// grok-4→grok、gpt-5→openai、seedream→newapi 偏向到对的平台。
	if hint := universalModelPlatformHint(model); hint != "" {
		hinted := eligible[:0:0]
		for _, g := range eligible {
			if g.Platform == hint {
				hinted = append(hinted, g)
			}
		}
		if len(hinted) > 0 {
			eligible = hinted
		}
	}

	return pickUniversalBackingGroup(eligible), nil
}

// span 取（带缓存的）用户权限跨度。命中且未过期 → 0 次 DB；未命中走 singleflight 合并重算。
func (r *UniversalRoutingResolver) span(ctx context.Context, userID int64) ([]Group, error) {
	r.mu.RLock()
	entry := r.cache[userID]
	r.mu.RUnlock()
	if entry != nil && time.Now().Before(entry.expires) {
		return entry.groups, nil
	}

	v, err, _ := r.sf.Do(strconv.FormatInt(userID, 10), func() (any, error) {
		groups, err := r.lister.GetAvailableGroups(ctx, userID)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		r.mu.Lock()
		// 写时清扫已过期条目,防止不再活跃的用户条目无限堆积(无需后台 goroutine)。
		// 仅在缓存超过软阈值时清扫,避免小缓存下每次 miss 的 O(n) 开销。
		if len(r.cache) > spanCacheSweepThreshold {
			for uid, e := range r.cache {
				if !now.Before(e.expires) {
					delete(r.cache, uid)
				}
			}
		}
		r.cache[userID] = &spanCacheEntry{groups: groups, expires: now.Add(r.jitteredTTL(userID))}
		r.mu.Unlock()
		return groups, nil
	})
	if err != nil {
		return nil, err
	}
	groups, _ := v.([]Group)
	return groups, nil
}

// Invalidate 主动失效某用户的跨度缓存（授权/订阅变更时调用；TTL 是兜底）。
// 由 APIKeyService.InvalidateAuthCacheByUserID 在用户授权变更时调用。
func (r *UniversalRoutingResolver) Invalidate(userID int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.cache, userID)
	r.mu.Unlock()
}

// InvalidateAll 清空整个跨度缓存（分组配置/状态变更影响面广时调用）。
// 由 APIKeyService.InvalidateAuthCacheByGroupID 调用。
func (r *UniversalRoutingResolver) InvalidateAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.cache = make(map[int64]*spanCacheEntry)
	r.mu.Unlock()
}

// jitteredTTL 在基础 TTL 上叠加由 userID 派生的抖动（0..jitterBuckets-1 秒），
// 摊开过期时刻避免同一秒集中 miss 打 DB。
func (r *UniversalRoutingResolver) jitteredTTL(userID int64) time.Duration {
	base := r.ttl
	if base <= 0 {
		base = defaultUniversalSpanTTL
	}
	bucket := userID % spanCacheJitterBuckets
	if bucket < 0 {
		bucket += spanCacheJitterBuckets
	}
	return base + time.Duration(bucket)*time.Second
}

// spanHasMessagesDispatch 报告跨度内是否有开了 messages-dispatch 的组（决定 /v1/messages
// 是否把 openai-compat 平台并入候选——用 Claude 名映射到 GPT 模型的场景）。
func spanHasMessagesDispatch(span []Group) bool {
	for i := range span {
		if span[i].AllowMessagesDispatch {
			return true
		}
	}
	return false
}

// pickUniversalBackingGroup 在候选后端组里按确定规则挑一个：持订阅优先 → sort_order → id。
// （跨度里的订阅型组都是“已持有订阅”的，因为 GetAvailableGroups 已过滤掉未持有的订阅组。）
func pickUniversalBackingGroup(eligible []Group) *Group {
	if len(eligible) == 0 {
		return nil
	}
	best := 0
	for i := 1; i < len(eligible); i++ {
		if lessUniversalBacking(eligible[i], eligible[best]) {
			best = i
		}
	}
	g := eligible[best]
	return &g
}

func lessUniversalBacking(a, b Group) bool {
	if as, bs := a.IsSubscriptionType(), b.IsSubscriptionType(); as != bs {
		return as // 持订阅的排前
	}
	if a.SortOrder != b.SortOrder {
		return a.SortOrder < b.SortOrder
	}
	return a.ID < b.ID
}

// UniversalNoEntitledGroupError 把内部哨兵错误包装成带 HTTP 语义（403）的业务错误，
// 供需要结构化错误码的调用方使用。middleware 直接按协议写形状，无需用它。
func UniversalNoEntitledGroupError(model string) error {
	msg := "no platform in your plan can serve this request"
	if model != "" {
		msg = "no platform in your plan can serve model " + strconv.Quote(model)
	}
	return infraerrors.Forbidden("UNIVERSAL_NO_ENTITLED_GROUP", msg)
}
