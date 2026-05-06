//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests for pricing_availability_predicate_tk.go (IsUnreachable).
//
// Low-severity review finding R-003 from docs/review-20260507 requested
// isolation tests for nil-safety and boundary conditions.

func TestIsUnreachable_NilReceiver(t *testing.T) {
	var s *PricingAvailabilityService
	require.False(t, s.IsUnreachable(context.Background(), "gemini", "gemini-2.5-flash"),
		"nil receiver must return false (fail-open, not panic)")
}

func TestIsUnreachable_NilRepo(t *testing.T) {
	// repo=nil means no persistence; IsUnreachable must be false (fail-open)
	svc := NewPricingAvailabilityService(nil, time.Now)
	require.False(t, svc.IsUnreachable(context.Background(), "gemini", "gemini-2.5-flash"),
		"nil repo must return false (fail-open)")
}

func TestIsUnreachable_EmptyPlatformOrModel(t *testing.T) {
	svc := NewPricingAvailabilityService(newMemoryRepo(), time.Now)
	require.False(t, svc.IsUnreachable(context.Background(), "", "gemini-2.5-flash"),
		"empty platform must return false")
	require.False(t, svc.IsUnreachable(context.Background(), "gemini", ""),
		"empty modelID must return false")
	require.False(t, svc.IsUnreachable(context.Background(), "   ", "gemini-2.5-flash"),
		"whitespace platform must return false")
}

func TestIsUnreachable_RepoError_FailOpen(t *testing.T) {
	// A repo that always returns an error must not make the predicate return true
	errRepo := &errorRepo{}
	svc := NewPricingAvailabilityService(errRepo, time.Now)
	require.False(t, svc.IsUnreachable(context.Background(), "gemini", "gemini-2.5-flash"),
		"repo error must be fail-open (return false, not panic)")
}

func TestIsUnreachable_UnreachableStatus_ReturnsTrue(t *testing.T) {
	repo := newMemoryRepo()
	svc := NewPricingAvailabilityService(repo, time.Now)

	// Drive the cell to unreachable via single model_not_found sample
	svc.RecordOutcome(context.Background(), AvailabilityOutcome{
		Platform:           "gemini",
		ModelID:            "gemini-old-model",
		Success:            false,
		UpstreamStatusCode: 404,
		UpstreamErrorBody:  `{"error": {"message": "Requested entity was not found."}}`,
	})

	require.True(t, svc.IsUnreachable(context.Background(), "gemini", "gemini-old-model"),
		"model with unreachable status must return true")
}

func TestIsUnreachable_OKStatus_ReturnsFalse(t *testing.T) {
	repo := newMemoryRepo()
	svc := NewPricingAvailabilityService(repo, time.Now)

	svc.RecordOutcome(context.Background(), AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.5-flash",
		Success: true, UpstreamStatusCode: 200,
	})

	require.False(t, svc.IsUnreachable(context.Background(), "gemini", "gemini-2.5-flash"),
		"model with ok status must return false")
}

func TestIsUnreachable_UntestedModel_ReturnsFalse(t *testing.T) {
	svc := NewPricingAvailabilityService(newMemoryRepo(), time.Now)
	// No RecordOutcome call for this model → untested / Status="" → fail-open
	require.False(t, svc.IsUnreachable(context.Background(), "gemini", "never-seen-model"),
		"untested model (status empty) must return false (fail-open)")
}

// errorRepo is a stub that always returns an error from Get.
type errorRepo struct{}

func (r *errorRepo) Upsert(_ context.Context, _, _ string, fn func(AvailabilityState) AvailabilityState) error {
	return errors.New("repo error")
}
func (r *errorRepo) Get(_ context.Context, _, _ string) (AvailabilityState, error) {
	return AvailabilityState{}, errors.New("repo error")
}
