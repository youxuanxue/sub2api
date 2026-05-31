package repository

import (
	"context"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// settingsPubSubChannel is the Redis pub/sub channel used to fan out a
// SystemSettings-write event to every replica. A small "refresh" signal, not
// the payload — peers reload from the DB on receipt (same shape as the tier /
// TLS-profile cache pub/sub).
const settingsPubSubChannel = "settings_updated"

// settingPubSub implements service.SettingPubSub over Redis. Lives in the
// repository layer so the service package stays redis-free (depguard).
type settingPubSub struct {
	rdb *redis.Client
}

// NewSettingPubSub builds the cross-replica settings fan-out bus. A nil client
// yields a no-op bus (single-replica dev: the 60s per-cache TTL is the fallback).
func NewSettingPubSub(rdb *redis.Client) service.SettingPubSub {
	return &settingPubSub{rdb: rdb}
}

func (p *settingPubSub) Publish(ctx context.Context) error {
	if p.rdb == nil {
		return nil
	}
	return p.rdb.Publish(ctx, settingsPubSubChannel, "refresh").Err()
}

func (p *settingPubSub) Subscribe(ctx context.Context, handler func()) {
	if p.rdb == nil {
		return
	}
	go func() {
		sub := p.rdb.Subscribe(ctx, settingsPubSubChannel)
		defer func() { _ = sub.Close() }()

		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				slog.Debug("settings_pubsub_subscriber_stopped", "reason", "context_done")
				return
			case msg := <-ch:
				if msg == nil {
					slog.Warn("settings_pubsub_subscriber_stopped", "reason", "channel_closed")
					return
				}
				handler()
			}
		}
	}()
}
