package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type escalationSlotCache struct {
	rdb *redis.Client
}

// NewEscalationSlotCache 创建渐进惩罚阶梯的共享升级槽位实现。
// 语义见 service.EscalationSlotCache：按故障事件而非错误次数推进阶梯。
func NewEscalationSlotCache(rdb *redis.Client) service.EscalationSlotCache {
	return &escalationSlotCache{rdb: rdb}
}

func escalationSlotKey(keyPrefix string, accountID int64) string {
	return fmt.Sprintf("%s%d", keyPrefix, accountID)
}

// AcquireEscalationSlot 原子 SET-if-absent 占位，返回本次调用是否抢到槽位。
func (c *escalationSlotCache) AcquireEscalationSlot(
	ctx context.Context, keyPrefix string, accountID int64, ttlSeconds int,
) (bool, error) {
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	ok, err := c.rdb.SetNX(
		ctx, escalationSlotKey(keyPrefix, accountID), 1, time.Duration(ttlSeconds)*time.Second,
	).Result()
	if err != nil {
		return false, fmt.Errorf("acquire escalation slot %s: %w", keyPrefix, err)
	}
	return ok, nil
}

// ShrinkEscalationSlotTTL 把槽位 TTL 收缩到实际落下的冷却长度。
func (c *escalationSlotCache) ShrinkEscalationSlotTTL(
	ctx context.Context, keyPrefix string, accountID int64, ttlSeconds int,
) error {
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	return c.rdb.Expire(
		ctx, escalationSlotKey(keyPrefix, accountID), time.Duration(ttlSeconds)*time.Second,
	).Err()
}

// ReleaseEscalationSlot 删除槽位。
func (c *escalationSlotCache) ReleaseEscalationSlot(
	ctx context.Context, keyPrefix string, accountID int64,
) error {
	return c.rdb.Del(ctx, escalationSlotKey(keyPrefix, accountID)).Err()
}
