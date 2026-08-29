//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests for pricing_availability_predicate_tk.go (IsStructurallyGone).

func TestIsStructurallyGone_NilReceiver(t *testing.T) {
	var s *PricingAvailabilityService
	require.False(t, s.IsStructurallyGone(context.Background(), "gemini", "gemini-2.5-flash"),
		"nil receiver must return false (fail-open, not panic)")
}

func TestIsStructurallyGone_NilRepo(t *testing.T) {
	// repo=nil means no persistence; IsStructurallyGone must be false (fail-open)
	svc := NewPricingAvailabilityService(nil, time.Now)
	require.False(t, svc.IsStructurallyGone(context.Background(), "gemini", "gemini-2.5-flash"),
		"nil repo must return false (fail-open)")
}

func TestIsStructurallyGone_EmptyPlatformOrModel(t *testing.T) {
	svc := NewPricingAvailabilityService(newMemoryRepo(), time.Now)
	require.False(t, svc.IsStructurallyGone(context.Background(), "", "gemini-2.5-flash"),
		"empty platform must return false")
	require.False(t, svc.IsStructurallyGone(context.Background(), "gemini", ""),
		"empty modelID must return false")
	require.False(t, svc.IsStructurallyGone(context.Background(), "   ", "gemini-2.5-flash"),
		"whitespace platform must return false")
}

func TestIsStructurallyGone_RepoError_FailOpen(t *testing.T) {
	// A repo that always returns an error must not make the predicate return true
	errRepo := &errorRepo{}
	svc := NewPricingAvailabilityService(errRepo, time.Now)
	require.False(t, svc.IsStructurallyGone(context.Background(), "gemini", "gemini-2.5-flash"),
		"repo error must be fail-open (return false, not panic)")
}

func TestIsStructurallyGone_ModelNotFound_ReturnsTrue(t *testing.T) {
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

	require.True(t, svc.IsStructurallyGone(context.Background(), "gemini", "gemini-old-model"),
		"model_not_found must be treated as structurally gone")
}

func TestIsStructurallyGone_TransientUnreachable_ReturnsFalse(t *testing.T) {
	repo := newMemoryRepo()
	svc := NewPricingAvailabilityService(repo, time.Now)

	svc.RecordOutcome(context.Background(), AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.5-flash",
		Success: false, UpstreamStatusCode: 503,
	})

	require.False(t, svc.IsStructurallyGone(context.Background(), "gemini", "gemini-2.5-flash"),
		"transient upstream failure must remain evidence, not structural removal")
}

func TestIsStructurallyGone_OKStatus_ReturnsFalse(t *testing.T) {
	repo := newMemoryRepo()
	svc := NewPricingAvailabilityService(repo, time.Now)

	svc.RecordOutcome(context.Background(), AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.5-flash",
		Success: true, UpstreamStatusCode: 200,
	})

	require.False(t, svc.IsStructurallyGone(context.Background(), "gemini", "gemini-2.5-flash"),
		"model with ok status must return false")
}

func TestIsStructurallyGone_UntestedModel_ReturnsFalse(t *testing.T) {
	svc := NewPricingAvailabilityService(newMemoryRepo(), time.Now)
	// No RecordOutcome call for this model → untested / Status="" → fail-open
	require.False(t, svc.IsStructurallyGone(context.Background(), "gemini", "never-seen-model"),
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
