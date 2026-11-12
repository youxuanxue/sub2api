//go:build unit

package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestRemapProtocolSelectionFailure(t *testing.T) {
	t.Parallel()

	capacity := newUniversalCapacityError(PlatformNewAPI, 47, nil)

	t.Run("capacity_for_protocol_unknown", func(t *testing.T) {
		for _, err := range []error{
			ErrProtocolCapabilityUnknown,
			fmt.Errorf("%w: %w", ErrProtocolRouteUnavailable, ErrProtocolCapabilityUnknown),
		} {
			got, ok := remapProtocolSelectionFailure(err, capacity)
			require.True(t, ok)
			require.ErrorIs(t, got, ErrUniversalCapacityUnavailable)
			require.NotErrorIs(t, got, ErrUniversalUnsupportedModel)
			require.NotErrorIs(t, got, ErrProtocolCapabilityUnknown)
		}
	})

	t.Run("unhandled_non_protocol", func(t *testing.T) {
		got, ok := remapProtocolSelectionFailure(errors.New("repository unavailable"), capacity)
		require.False(t, ok)
		require.Nil(t, got)
	})
}

func TestCandidateSelectionError_ProtocolDoesNotOpaque500(t *testing.T) {
	t.Parallel()

	t.Run("unsupported_request_when_capability_unknown", func(t *testing.T) {
		err := candidateSelectionError(false, ErrProtocolCapabilityUnknown, "deepseek-v4-pro", PlatformNewAPI, 47, nil)
		require.ErrorIs(t, err, ErrUniversalUnsupportedRequest)
		require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
		require.NotErrorIs(t, err, ErrProtocolCapabilityUnknown)
		require.NotErrorIs(t, err, ErrUniversalUnsupportedModel)
	})

	t.Run("unsupported_request_when_nothing_supported", func(t *testing.T) {
		err := candidateSelectionError(false, ErrProtocolRouteUnavailable, "deepseek-v4.1-flash", PlatformNewAPI, 3, nil)
		require.ErrorIs(t, err, ErrUniversalUnsupportedRequest)
		require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
		require.NotErrorIs(t, err, ErrUniversalUnsupportedModel)
		require.Equal(t, "Unsupported request for model: deepseek-v4.1-flash", TkUnsupportedRequestMessage("deepseek-v4.1-flash"))
	})

	t.Run("unsupported_request_when_support_count_is_zero", func(t *testing.T) {
		diag := &CandidateCapacityDiag{AccountTotal: 29, Supported: 0, Ready: 0}
		err := candidateSelectionError(true, nil, "gemini-4-argon", PlatformAntigravity, 21, diag)
		require.ErrorIs(t, err, ErrUniversalUnsupportedRequest)
		require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
		require.NotErrorIs(t, err, ErrUniversalUnsupportedModel)
	})

	t.Run("capacity_when_supported_but_not_ready", func(t *testing.T) {
		diag := &CandidateCapacityDiag{AccountTotal: 15, Supported: 7, Ready: 0}
		diag.reject("temp_unschedulable")
		err := candidateSelectionError(true, nil, "gemini-4-argon", PlatformAntigravity, 21, diag)
		require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
		require.NotErrorIs(t, err, ErrUniversalUnsupportedRequest)
		require.Equal(t, 7, CandidateCapacityDiagFromError(err).Supported)
		require.Equal(t, 0, CandidateCapacityDiagFromError(err).Ready)
		require.Equal(t, "temp_unschedulable=1", CandidateCapacityDiagFromError(err).RejectSummary())
	})

	t.Run("capacity_when_supported_but_exhausted", func(t *testing.T) {
		err := candidateSelectionError(true, ErrProtocolRouteUnavailable, "deepseek-v4-pro", PlatformNewAPI, 47, nil)
		require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
		require.NotErrorIs(t, err, ErrProtocolRouteUnavailable)
	})

	t.Run("capacity_when_supported_despite_unknown_peer", func(t *testing.T) {
		err := candidateSelectionError(true, ErrProtocolCapabilityUnknown, "deepseek-v4-pro", PlatformNewAPI, 47, nil)
		require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
		require.NotErrorIs(t, err, ErrUniversalUnsupportedRequest)
	})

	t.Run("no_legal_route_with_supported_falls_to_capacity", func(t *testing.T) {
		err := candidateSelectionError(true, protocolrouter.ErrNoLegalRoute, "deepseek-v4-pro", PlatformNewAPI, 47, nil)
		require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
	})

	t.Run("non_protocol_internal_still_surfaces", func(t *testing.T) {
		internal := errors.New("database unavailable")
		err := candidateSelectionError(false, internal, "deepseek-v4-pro", PlatformNewAPI, 47, nil)
		require.ErrorIs(t, err, internal)
		require.NotErrorIs(t, err, ErrUniversalUnsupportedModel)
		require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
	})
}
