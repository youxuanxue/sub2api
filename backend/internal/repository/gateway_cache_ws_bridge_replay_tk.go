package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openAIWSBridgeReplayPrefix = "openai:ws-bridge-replay:v1:"

var _ service.OpenAIWSBridgeReplayCache = (*gatewayCache)(nil)

func (c *gatewayCache) SetOpenAIWSBridgeReplay(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if key == "" || len(payload) == 0 || len(payload) > service.OpenAIWSBridgeReplayMaxBytes || ttl <= 0 {
		return errors.New("invalid websocket bridge replay")
	}
	return c.rdb.Set(ctx, openAIWSBridgeReplayPrefix+key, payload, ttl).Err()
}

func (c *gatewayCache) GetOpenAIWSBridgeReplay(ctx context.Context, key string) ([]byte, error) {
	if key == "" {
		return nil, nil
	}
	raw, err := c.rdb.Get(ctx, openAIWSBridgeReplayPrefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return raw, err
}
