package batchworker

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvideManagedBatchRuntimeDefaultIsStopped(t *testing.T) {
	runtime := ProvideManagedBatchRuntime(nil, nil, nil, nil, nil, nil, service.NewProtocolRouter(), &config.Config{})
	require.NotNil(t, runtime)
	require.False(t, runtime.Running())
}
