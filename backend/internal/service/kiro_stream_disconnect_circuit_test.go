//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKiroStreamDisconnectCircuitThresholdWindowCooldownAndSuccessReset(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newKiroStreamDisconnectCircuit(kiroStreamDisconnectCircuitSettings{
		failureThreshold: 2,
		failureWindow:    time.Minute,
		cooldown:         10 * time.Minute,
		maxEntries:       16,
	})

	tripped, _ := circuit.recordFailure(1, base)
	require.False(t, tripped)
	require.False(t, circuit.isBlocked(1, base))
	require.True(t, circuit.recordSuccess(1))

	tripped, _ = circuit.recordFailure(1, base.Add(10*time.Second))
	require.False(t, tripped, "a terminal success must clear the previous failure")
	tripped, until := circuit.recordFailure(1, base.Add(20*time.Second))
	require.True(t, tripped)
	require.Equal(t, base.Add(20*time.Second+10*time.Minute), until)
	require.True(t, circuit.isBlocked(1, until.Add(-time.Nanosecond)))
	require.False(t, circuit.isBlocked(1, until), "cooldown expiry must re-admit the account")

	tripped, _ = circuit.recordFailure(2, base)
	require.False(t, tripped)
	tripped, _ = circuit.recordFailure(2, base.Add(2*time.Minute))
	require.False(t, tripped, "failures outside the window must not accumulate")
}

func TestKiroStreamDisconnectCircuitBoundsEntries(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	circuit := newKiroStreamDisconnectCircuit(kiroStreamDisconnectCircuitSettings{
		failureThreshold: 1,
		failureWindow:    time.Minute,
		cooldown:         10 * time.Minute,
		maxEntries:       2,
	})

	circuit.recordFailure(1, base)
	circuit.recordFailure(2, base.Add(time.Second))
	circuit.recordFailure(3, base.Add(2*time.Second))

	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	require.Len(t, circuit.entries, 2)
	_, oldestRetained := circuit.entries[1]
	require.False(t, oldestRetained, "the oldest entry must be evicted at the bound")
}

func TestShouldRecordKiroStreamDisconnect(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{name: "unexpected EOF", ctx: context.Background(), err: io.ErrUnexpectedEOF, want: true},
		{name: "HTTP2 connection lost", ctx: context.Background(), err: errors.New("http2: client connection lost"), want: true},
		{name: "client canceled", ctx: context.Background(), err: context.Canceled, want: false},
		{name: "request context canceled", ctx: canceledCtx, err: io.ErrUnexpectedEOF, want: false},
		{name: "provider exception", ctx: context.Background(), err: errors.New("kiro event stream error: throttlingException"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldRecordKiroStreamDisconnect(tt.ctx, tt.err))
		})
	}
}
