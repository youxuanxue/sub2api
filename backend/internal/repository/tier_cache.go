package repository

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Tier cache mirrors tlsFingerprintProfileCache: a Redis-backed snapshot with a
// pub/sub "refresh" channel so a tier-row edit fans out to every replica within
// seconds (accounts reference tier by id, so zero account writes are needed).
const (
	tierCacheKey  = "tiers"
	tierPubSubKey = "tiers_updated"
	tierCacheTTL  = 24 * time.Hour
)

type tierCache struct {
	rdb        *redis.Client
	localCache []*model.Tier
	localMu    sync.RWMutex
}

// NewTierCache 创建 tier 缓存。
func NewTierCache(rdb *redis.Client) service.TierCache {
	return &tierCache{rdb: rdb}
}

func (c *tierCache) Get(ctx context.Context) ([]*model.Tier, bool) {
	c.localMu.RLock()
	if c.localCache != nil {
		tiers := c.localCache
		c.localMu.RUnlock()
		return tiers, true
	}
	c.localMu.RUnlock()

	if c.rdb == nil {
		return nil, false
	}
	data, err := c.rdb.Get(ctx, tierCacheKey).Bytes()
	if err != nil {
		if err != redis.Nil {
			slog.Warn("tier_cache_get_failed", "error", err)
		}
		return nil, false
	}
	var tiers []*model.Tier
	if err := json.Unmarshal(data, &tiers); err != nil {
		slog.Warn("tier_cache_unmarshal_failed", "error", err)
		return nil, false
	}
	c.localMu.Lock()
	c.localCache = tiers
	c.localMu.Unlock()
	return tiers, true
}

func (c *tierCache) Set(ctx context.Context, tiers []*model.Tier) error {
	data, err := json.Marshal(tiers)
	if err != nil {
		return err
	}
	if c.rdb != nil {
		if err := c.rdb.Set(ctx, tierCacheKey, data, tierCacheTTL).Err(); err != nil {
			return err
		}
	}
	c.localMu.Lock()
	c.localCache = tiers
	c.localMu.Unlock()
	return nil
}

func (c *tierCache) Invalidate(ctx context.Context) error {
	c.localMu.Lock()
	c.localCache = nil
	c.localMu.Unlock()
	if c.rdb == nil {
		return nil
	}
	return c.rdb.Del(ctx, tierCacheKey).Err()
}

func (c *tierCache) NotifyUpdate(ctx context.Context) error {
	if c.rdb == nil {
		return nil
	}
	return c.rdb.Publish(ctx, tierPubSubKey, "refresh").Err()
}

func (c *tierCache) SubscribeUpdates(ctx context.Context, handler func()) {
	if c.rdb == nil {
		return
	}
	go func() {
		sub := c.rdb.Subscribe(ctx, tierPubSubKey)
		defer func() { _ = sub.Close() }()

		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				slog.Debug("tier_cache_subscriber_stopped", "reason", "context_done")
				return
			case msg := <-ch:
				if msg == nil {
					slog.Warn("tier_cache_subscriber_stopped", "reason", "channel_closed")
					return
				}
				c.localMu.Lock()
				c.localCache = nil
				c.localMu.Unlock()
				handler()
			}
		}
	}()
}
