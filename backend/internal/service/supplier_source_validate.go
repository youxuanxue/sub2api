package service

import (
	"context"
)

// SupplierSourceValidateResult is the read-only outcome of validating saved supplier models.
type SupplierSourceValidateResult struct {
	SourceID     int64                 `json:"source_id"`
	ProbeResults []SupplierProbeResult `json:"probe_results"`
	FailedStep   string                `json:"failed_step,omitempty"`
}

// Discover lists/normalizes upstream models and probes unconfigured candidates. It never writes
// the supplier source or accounts, and does not probe configured rows.
func (s *SupplierSourceService) Discover(ctx context.Context, sourceID int64) (*SupplierSourceProbeResult, error) {
	return s.StartSupplierProbeJob(ctx, sourceID)
}

// Validate probes configured supplier models for servability as a read-only preview. Sync always
// performs its own probe immediately before any structural projection write.
func (s *SupplierSourceService) Validate(ctx context.Context, sourceID int64) (*SupplierSourceValidateResult, error) {
	result := &SupplierSourceValidateResult{
		SourceID: sourceID, ProbeResults: make([]SupplierProbeResult, 0),
	}
	if s == nil || s.repo == nil || sourceID <= 0 {
		result.FailedStep = "validate_request"
		return result, ErrSupplierSourceInvalidInput
	}
	source, err := s.repo.Get(ctx, sourceID)
	if err != nil {
		result.FailedStep = "load_source"
		return result, err
	}
	if len(source.Models) == 0 {
		return result, nil
	}
	probeResults, probeErr := s.probeConfiguredSourceModels(ctx, sourceID)
	result.ProbeResults = probeResults
	if probeErr != nil {
		result.FailedStep = "validate"
		return result, probeErr
	}
	return result, nil
}

// GetSupplierDiscoverJob returns the latest snapshot for an async discover job.
func (s *SupplierSourceService) GetSupplierDiscoverJob(ctx context.Context, sourceID int64, jobID string) (*SupplierSourceProbeResult, error) {
	return s.GetSupplierProbeJob(ctx, sourceID, jobID)
}
