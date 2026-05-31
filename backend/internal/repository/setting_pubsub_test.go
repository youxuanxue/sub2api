//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Publish on one client must invoke the Subscribe handler on another, proving
// the cross-replica settings fan-out works end-to-end over Redis.
func TestSettingPubSub_PublishDeliversToSubscriber(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus := NewSettingPubSub(rdb)
	fired := make(chan struct{}, 4)
	bus.Subscribe(ctx, func() { fired <- struct{}{} })

	// Wait for the subscription to register before publishing.
	require.Eventually(t, func() bool {
		return rdb.PubSubNumSub(ctx, settingsPubSubChannel).Val()[settingsPubSubChannel] == 1
	}, 2*time.Second, 10*time.Millisecond, "subscriber did not register")

	require.NoError(t, bus.Publish(ctx))

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("publish did not reach the subscriber")
	}
}

// A nil client must yield a no-op bus (single-replica dev), never a panic.
func TestSettingPubSub_NilClientIsNoop(t *testing.T) {
	bus := NewSettingPubSub(nil)
	require.NoError(t, bus.Publish(context.Background()))
	bus.Subscribe(context.Background(), func() { t.Fatal("handler should never fire on nil bus") })
}
