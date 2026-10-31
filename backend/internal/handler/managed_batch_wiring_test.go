package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestProvideTKManagedBatchHandlerKeepsBackendClosedUntilWired(t *testing.T) {
	// The config gate alone must never expose a partial executor. The provider
	// intentionally returns a nil-backend-safe handler until the repository,
	// private object store, queue, runner and billing seams are supplied.
	h := ProvideTKManagedBatchHandler(&config.Config{
		ManagedBatch: config.ManagedBatchConfig{Enabled: true},
	}, nil)
	require.NotNil(t, h)
	require.True(t, h.Enabled)
	require.Nil(t, h.Backend)
}

func TestProvideTKManagedBatchHandlerDefaultsClosed(t *testing.T) {
	h := ProvideTKManagedBatchHandler(&config.Config{}, nil)
	require.NotNil(t, h)
	require.False(t, h.Enabled)
	require.Nil(t, h.Backend)
}
