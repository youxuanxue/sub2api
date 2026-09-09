package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheWSBridgeReplaySurvivesReaderAndExpires(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	writer, reader := &gatewayCache{rdb: client}, &gatewayCache{rdb: client}
	ctx := context.Background()
	const key = "user-owned-response-hash"
	raw := []byte(`{"version":1,"account_id":65,"input":[{"role":"user","content":"marker"}]}`)
	require.NoError(t, writer.SetOpenAIWSBridgeReplay(ctx, key, raw, time.Minute))
	got, err := reader.GetOpenAIWSBridgeReplay(ctx, key)
	require.NoError(t, err)
	require.Equal(t, raw, got)
	server.FastForward(time.Minute)
	got, err = reader.GetOpenAIWSBridgeReplay(ctx, key)
	require.NoError(t, err)
	require.Nil(t, got)
	server.Close()
	_, err = reader.GetOpenAIWSBridgeReplay(ctx, key)
	require.Error(t, err)
}
