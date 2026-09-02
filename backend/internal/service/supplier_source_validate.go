package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const supplierValidateCacheTTL = supplierProbeJobTTL

// SupplierSourceValidateResult is the read-only outcome of validating saved supplier models.
type SupplierSourceValidateResult struct {
	SourceID     int64                 `json:"source_id"`
	ProbeResults []SupplierProbeResult `json:"probe_results"`
	FailedStep   string                `json:"failed_step,omitempty"`
}

type supplierValidateCacheEntry struct {
	fingerprint string
	passed      bool
	expiresAt   time.Time
}

type supplierValidateCache struct {
	mu       sync.Mutex
	bySource map[int64]*supplierValidateCacheEntry
}

func newSupplierValidateCache() *supplierValidateCache {
	return &supplierValidateCache{bySource: make(map[int64]*supplierValidateCacheEntry)}
}

func (c *supplierValidateCache) put(sourceID int64, fingerprint string, passed bool) {
	if c == nil || sourceID <= 0 || strings.TrimSpace(fingerprint) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bySource[sourceID] = &supplierValidateCacheEntry{
		fingerprint: fingerprint,
		passed:      passed,
		expiresAt:   time.Now().Add(supplierValidateCacheTTL),
	}
}

func (c *supplierValidateCache) get(sourceID int64, fingerprint string) (bool, bool) {
	if c == nil || sourceID <= 0 || strings.TrimSpace(fingerprint) == "" {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.bySource[sourceID]
	if entry == nil || time.Now().After(entry.expiresAt) {
		delete(c.bySource, sourceID)
		return false, false
	}
	if entry.fingerprint != fingerprint {
		return false, false
	}
	return entry.passed, true
}

func (c *supplierValidateCache) invalidate(sourceID int64) {
	if c == nil || sourceID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.bySource, sourceID)
}

func supplierValidateFingerprint(source *SupplierSource) string {
	if source == nil {
		return ""
	}
	parts := []string{
		source.CredentialFingerprint,
		source.Endpoint,
		fmt.Sprintf("%d", source.ChannelType),
	}
	for _, model := range source.Models {
		parts = append(parts, model.ClientModelID, model.UpstreamModelID)
		if model.PurchaseRatio != nil {
			parts = append(parts, fmt.Sprintf("%g", *model.PurchaseRatio))
		}
	}
	return strings.Join(parts, "\x00")
}

func (s *SupplierSourceService) validateCacheRegistry() *supplierValidateCache {
	if s == nil {
		return nil
	}
	if s.validateCache == nil {
		s.validateCache = newSupplierValidateCache()
	}
	return s.validateCache
}

func (s *SupplierSourceService) invalidateSupplierValidateCache(sourceID int64) {
	if s == nil {
		return
	}
	s.validateCacheRegistry().invalidate(sourceID)
}

// Discover lists/normalizes upstream models and probes unconfigured candidates. It never writes
// the supplier source or accounts, and does not probe configured rows.
func (s *SupplierSourceService) Discover(ctx context.Context, sourceID int64) (*SupplierSourceProbeResult, error) {
	return s.StartSupplierProbeJob(ctx, sourceID)
}

// Validate probes configured supplier models for servability. It never writes accounts; a
// successful result is cached briefly for Sync projection.
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
		s.validateCacheRegistry().put(source.ID, supplierValidateFingerprint(source), true)
		return result, nil
	}
	probeResults, probeErr := s.probeConfiguredSourceModels(ctx, sourceID)
	result.ProbeResults = probeResults
	if probeErr != nil {
		result.FailedStep = "validate"
		s.invalidateSupplierValidateCache(sourceID)
		return result, probeErr
	}
	s.validateCacheRegistry().put(source.ID, supplierValidateFingerprint(source), true)
	return result, nil
}

// GetSupplierDiscoverJob returns the latest snapshot for an async discover job.
func (s *SupplierSourceService) GetSupplierDiscoverJob(ctx context.Context, sourceID int64, jobID string) (*SupplierSourceProbeResult, error) {
	return s.GetSupplierProbeJob(ctx, sourceID, jobID)
}
