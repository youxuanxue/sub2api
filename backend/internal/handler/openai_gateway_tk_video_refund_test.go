//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestShouldRetryVideoRefund locks the bound of the terminal-failure refund
// re-attempt loop: only a still-pending billed row is retryable, and only below
// the attempt cap. A bug here either drops a recoverable refund early or loops
// past the bound; the money math + idempotency are tested in the service package.
func TestShouldRetryVideoRefund(t *testing.T) {
	// Only OriginPending is retryable.
	require.True(t, shouldRetryVideoRefund(service.VideoRefundOriginPending, 0))
	require.True(t, shouldRetryVideoRefund(service.VideoRefundOriginPending, videoRefundMaxAttempts-2))
	// The final allowed attempt must not schedule another (attempt is 0-based).
	require.False(t, shouldRetryVideoRefund(service.VideoRefundOriginPending, videoRefundMaxAttempts-1))
	require.False(t, shouldRetryVideoRefund(service.VideoRefundOriginPending, videoRefundMaxAttempts))

	// No non-pending outcome ever retries.
	for _, o := range []service.VideoRefundOutcome{
		service.VideoRefundApplied,
		service.VideoRefundAlreadyApplied,
		service.VideoRefundNothing,
		service.VideoRefundSkipped,
		service.VideoRefundFailed,
	} {
		require.False(t, shouldRetryVideoRefund(o, 0))
	}
}
