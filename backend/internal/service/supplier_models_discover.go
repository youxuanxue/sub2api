package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	supplierDiscoverDefaultPurchaseRatio = 1.0
	// Concurrent live Chat probes for one discover job. Admin-only, long-running.
	supplierDiscoverProbeConcurrency = 8
	supplierDiscoverJobTTL           = 30 * time.Minute
	supplierDiscoverProbeTimeout     = 15 * time.Minute
)

// SupplierDiscoverProbeStatus is the async candidate-probe lifecycle for models-discover.
type SupplierDiscoverProbeStatus string

const (
	SupplierDiscoverProbePending   SupplierDiscoverProbeStatus = "pending"
	SupplierDiscoverProbeRunning   SupplierDiscoverProbeStatus = "running"
	SupplierDiscoverProbeCompleted SupplierDiscoverProbeStatus = "completed"
	SupplierDiscoverProbeFailed    SupplierDiscoverProbeStatus = "failed"
)

// SupplierModelNormalizeChange records a configured-row rewrite to a canonical upstream id.
type SupplierModelNormalizeChange struct {
	FromClientModelID   string `json:"from_client_model_id"`
	FromUpstreamModelID string `json:"from_upstream_model_id"`
	ToClientModelID     string `json:"to_client_model_id"`
	ToUpstreamModelID   string `json:"to_upstream_model_id"`
}

// SupplierModelDiscoverIssue is a configured model that cannot be matched to the upstream list.
type SupplierModelDiscoverIssue struct {
	ClientModelID   string `json:"client_model_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	Reason          string `json:"reason"`
}

// SupplierModelDiscoverRejection is an upstream-only candidate that was not suggested.
type SupplierModelDiscoverRejection struct {
	UpstreamModelID string `json:"upstream_model_id"`
	Type            string `json:"type,omitempty"`
	Reason          string `json:"reason"`
	Detail          string `json:"detail,omitempty"`
}

// SupplierModelsDiscoverResult is a read-only preview used by “校验并同步” before account projection.
// List + normalize return immediately; candidate Chat probes continue asynchronously under JobID.
// It never writes accounts or the supplier source row. The UI applies NormalizedModels when
// NeedsConfirmation is set; SuggestedAppends stay opt-in via an explicit form action.
type SupplierModelsDiscoverResult struct {
	SourceID           int64                            `json:"source_id"`
	JobID              string                           `json:"job_id,omitempty"`
	ProbeStatus        SupplierDiscoverProbeStatus      `json:"probe_status"`
	ProbeTotal         int                              `json:"probe_total"`
	ProbeDone          int                              `json:"probe_done"`
	UpstreamModels     []SupplierUpstreamModelEntry     `json:"upstream_models"`
	NormalizedModels   []SupplierSourceModel            `json:"normalized_models"`
	NormalizedChanges  []SupplierModelNormalizeChange   `json:"normalized_changes"`
	SuggestedAppends   []SupplierSourceModel            `json:"suggested_appends"`
	RejectedCandidates []SupplierModelDiscoverRejection `json:"rejected_candidates"`
	ConfiguredIssues   []SupplierModelDiscoverIssue     `json:"configured_issues"`
	ProbeResults       []SupplierProbeResult            `json:"probe_results"`
	NeedsConfirmation  bool                             `json:"needs_confirmation"`
	FailedStep         string                           `json:"failed_step,omitempty"`
}

type supplierDiscoverJob struct {
	cancel    context.CancelFunc
	mu        sync.Mutex
	result    *SupplierModelsDiscoverResult
	err       error
	seen      map[string]struct{}
	createdAt time.Time
}

type supplierDiscoverJobRegistry struct {
	mu       sync.Mutex
	byID     map[string]*supplierDiscoverJob
	bySource map[int64]string
}

func newSupplierDiscoverJobRegistry() *supplierDiscoverJobRegistry {
	return &supplierDiscoverJobRegistry{
		byID:     make(map[string]*supplierDiscoverJob),
		bySource: make(map[int64]string),
	}
}

func (s *SupplierSourceService) discoverJobRegistry() *supplierDiscoverJobRegistry {
	if s == nil {
		return nil
	}
	if s.discoverJobs == nil {
		s.discoverJobs = newSupplierDiscoverJobRegistry()
	}
	return s.discoverJobs
}

// StartDiscoverModels lists/normalizes synchronously, then probes every probeable upstream
// candidate asynchronously with high concurrency. Poll GetDiscoverModelsJob until ProbeStatus
// is completed or failed. SuggestedAppends only include probe-passed models.
func (s *SupplierSourceService) StartDiscoverModels(ctx context.Context, sourceID int64) (*SupplierModelsDiscoverResult, error) {
	result := emptySupplierModelsDiscoverResult(sourceID)
	if s == nil || s.repo == nil || s.probe == nil || s.encryptor == nil || sourceID <= 0 {
		result.FailedStep = "validate_request"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, ErrSupplierSourceInvalidInput
	}
	lister, ok := s.probe.(SupplierUpstreamModelsLister)
	if !ok || lister == nil {
		result.FailedStep = "models_lister"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, ErrSupplierSourceInvalidInput
	}
	source, err := s.repo.Get(ctx, sourceID)
	if err != nil {
		result.FailedStep = "load_source"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, err
	}
	credential, err := s.encryptor.Decrypt(source.EncryptedCredential)
	if err != nil {
		result.FailedStep = "decrypt_credential"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, fmt.Errorf("decrypt supplier credential: %w", err)
	}
	upstream, err := lister.ListSupplierUpstreamModels(ctx, source.Endpoint, credential)
	if err != nil {
		result.FailedStep = "list_upstream_models"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, err
	}
	result.UpstreamModels = upstream

	coveredIDs := make(map[string]struct{}, len(source.Models))
	coveredKeys := make(map[string]struct{}, len(source.Models))
	for _, model := range source.Models {
		normalized := SupplierSourceModel{
			ClientModelID:   model.ClientModelID,
			UpstreamModelID: model.UpstreamModelID,
			PurchaseRatio:   cloneSupplierFloat64Ptr(model.PurchaseRatio),
		}
		lookup := model.UpstreamModelID
		if strings.TrimSpace(lookup) == "" {
			lookup = model.ClientModelID
		}
		canonical, matched := matchSupplierUpstreamModelID(lookup, upstream)
		if !matched {
			canonical, matched = matchSupplierUpstreamModelID(model.ClientModelID, upstream)
		}
		if !matched {
			result.ConfiguredIssues = append(result.ConfiguredIssues, SupplierModelDiscoverIssue{
				ClientModelID: model.ClientModelID, UpstreamModelID: model.UpstreamModelID,
				Reason: "not_in_upstream_list",
			})
			result.NormalizedModels = append(result.NormalizedModels, normalized)
			continue
		}
		coveredIDs[canonical] = struct{}{}
		coveredKeys[supplierModelMatchKey(canonical)] = struct{}{}
		nextClient := model.ClientModelID
		if supplierConfiguredClientShouldNormalize(model.ClientModelID, model.UpstreamModelID, canonical) {
			nextClient = canonical
		}
		nextUpstream := canonical
		if model.ClientModelID != nextClient || model.UpstreamModelID != nextUpstream {
			result.NormalizedChanges = append(result.NormalizedChanges, SupplierModelNormalizeChange{
				FromClientModelID: model.ClientModelID, FromUpstreamModelID: model.UpstreamModelID,
				ToClientModelID: nextClient, ToUpstreamModelID: nextUpstream,
			})
			normalized.ClientModelID = nextClient
			normalized.UpstreamModelID = nextUpstream
		}
		result.NormalizedModels = append(result.NormalizedModels, normalized)
	}

	candidates := make([]SupplierUpstreamModelEntry, 0)
	for _, entry := range upstream {
		if _, exists := coveredIDs[entry.ID]; exists {
			continue
		}
		if _, exists := coveredKeys[supplierModelMatchKey(entry.ID)]; exists {
			continue
		}
		candidates = append(candidates, entry)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	priority, priorityErr := SupplierAccountPriority(source.BasePriority, 6)
	if priorityErr != nil {
		result.FailedStep = "build_probe_account"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, priorityErr
	}
	probeAccount := supplierProbeAccount(nil, source, credential, supplierTargetBand{
		Band: 6, Priority: priority, Mapping: map[string]string{},
	})
	probeAccount.ID = supplierDiscoverProbeAccountID(source.ID)
	probeAccount.Concurrency = supplierDiscoverProbeConcurrency

	probeable := make([]SupplierUpstreamModelEntry, 0, len(candidates))
	for _, entry := range candidates {
		if !supplierUpstreamTypeProbeable(entry.Type) {
			result.RejectedCandidates = append(result.RejectedCandidates, SupplierModelDiscoverRejection{
				UpstreamModelID: entry.ID, Type: entry.Type, Reason: "non_chat_type",
			})
			continue
		}
		probeable = append(probeable, entry)
	}

	result.NeedsConfirmation = len(result.NormalizedChanges) > 0
	result.ProbeTotal = len(probeable)
	if len(probeable) == 0 {
		result.ProbeStatus = SupplierDiscoverProbeCompleted
		result.ProbeDone = 0
		return cloneSupplierModelsDiscoverResult(result), nil
	}

	jobID, err := newSupplierDiscoverJobID()
	if err != nil {
		result.FailedStep = "create_job"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, fmt.Errorf("create discover job id: %w", err)
	}
	result.JobID = jobID
	result.ProbeStatus = SupplierDiscoverProbeRunning
	result.ProbeDone = 0

	jobCtx, cancel := context.WithTimeout(context.Background(), supplierDiscoverProbeTimeout)
	job := &supplierDiscoverJob{
		cancel:    cancel,
		result:    cloneSupplierModelsDiscoverResult(result),
		createdAt: time.Now().UTC(),
	}
	s.discoverJobRegistry().put(sourceID, jobID, job)
	go s.runSupplierDiscoverProbes(jobCtx, job, probeAccount, probeable)
	return cloneSupplierModelsDiscoverResult(job.snapshot()), nil
}

// DiscoverModels starts async discover and, for callers that expect a finished snapshot
// (unit tests), waits until the probe job completes. HTTP handlers should use
// StartDiscoverModels + GetDiscoverModelsJob instead.
func (s *SupplierSourceService) DiscoverModels(ctx context.Context, sourceID int64) (*SupplierModelsDiscoverResult, error) {
	result, err := s.StartDiscoverModels(ctx, sourceID)
	if err != nil {
		return result, err
	}
	if result == nil || result.JobID == "" || result.ProbeStatus != SupplierDiscoverProbeRunning {
		return result, nil
	}
	deadline := time.Now().Add(supplierDiscoverProbeTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		next, getErr := s.GetDiscoverModelsJob(ctx, sourceID, result.JobID)
		if getErr != nil {
			return next, getErr
		}
		result = next
		if result.ProbeStatus == SupplierDiscoverProbeCompleted {
			return result, nil
		}
		if result.ProbeStatus == SupplierDiscoverProbeFailed {
			if result.FailedStep == "probe_candidate" {
				return result, ErrSupplierSourceProbeFailed
			}
			return result, fmt.Errorf("supplier discover probe failed")
		}
	}
	result.FailedStep = "probe_timeout"
	result.ProbeStatus = SupplierDiscoverProbeFailed
	return result, fmt.Errorf("supplier discover probe timed out")
}

// GetDiscoverModelsJob returns the latest snapshot for an async models-discover job.
func (s *SupplierSourceService) GetDiscoverModelsJob(ctx context.Context, sourceID int64, jobID string) (*SupplierModelsDiscoverResult, error) {
	_ = ctx
	if s == nil || sourceID <= 0 || strings.TrimSpace(jobID) == "" {
		return emptySupplierModelsDiscoverResult(sourceID), ErrSupplierSourceInvalidInput
	}
	job, ok := s.discoverJobRegistry().get(sourceID, jobID)
	if !ok || job == nil {
		result := emptySupplierModelsDiscoverResult(sourceID)
		result.JobID = jobID
		result.FailedStep = "job_not_found"
		result.ProbeStatus = SupplierDiscoverProbeFailed
		return result, ErrSupplierSourceNotFound
	}
	result := job.snapshot()
	return result, nil
}

func (s *SupplierSourceService) runSupplierDiscoverProbes(
	ctx context.Context,
	job *supplierDiscoverJob,
	probeAccount *Account,
	probeable []SupplierUpstreamModelEntry,
) {
	defer job.cancel()
	defaultRatio := supplierDiscoverDefaultPurchaseRatio
	var authFailed atomic.Bool
	sem := make(chan struct{}, supplierDiscoverProbeConcurrency)
	var wg sync.WaitGroup
	for _, entry := range probeable {
		wg.Add(1)
		go func(entry SupplierUpstreamModelEntry) {
			defer wg.Done()
			if authFailed.Load() {
				job.markProbeSkipped(entry, "canceled")
				return
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				job.markProbeSkipped(entry, "canceled")
				return
			}
			defer func() { <-sem }()
			if authFailed.Load() {
				job.markProbeSkipped(entry, "canceled")
				return
			}
			probeResult := s.probe.ProbeSupplierModel(ctx, SupplierProbeInput{
				Account: probeAccount, ClientModelID: entry.ID, UpstreamModelID: entry.ID,
			})
			probeResult.ClientModelID = entry.ID
			probeResult.UpstreamModelID = entry.ID
			if probeResult.Status == SupplierProbeStatusAuthFailed {
				authFailed.Store(true)
				job.cancel()
				job.applyProbeOutcome(entry, probeResult, &defaultRatio, true)
				return
			}
			job.applyProbeOutcome(entry, probeResult, &defaultRatio, false)
		}(entry)
	}
	wg.Wait()
	job.finish(authFailed.Load())
}

func (r *supplierDiscoverJobRegistry) put(sourceID int64, jobID string, job *supplierDiscoverJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictExpiredLocked(time.Now().UTC())
	if prevID, ok := r.bySource[sourceID]; ok {
		if prev, exists := r.byID[prevID]; exists && prev != nil {
			prev.cancel()
			delete(r.byID, prevID)
		}
	}
	r.byID[jobID] = job
	r.bySource[sourceID] = jobID
}

func (r *supplierDiscoverJobRegistry) get(sourceID int64, jobID string) (*supplierDiscoverJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictExpiredLocked(time.Now().UTC())
	job, ok := r.byID[jobID]
	if !ok || job == nil {
		return nil, false
	}
	if current, exists := r.bySource[sourceID]; !exists || current != jobID {
		return nil, false
	}
	return job, true
}

func (r *supplierDiscoverJobRegistry) evictExpiredLocked(now time.Time) {
	for id, job := range r.byID {
		if job == nil || now.Sub(job.createdAt) <= supplierDiscoverJobTTL {
			continue
		}
		job.cancel()
		delete(r.byID, id)
		for sourceID, jobID := range r.bySource {
			if jobID == id {
				delete(r.bySource, sourceID)
			}
		}
	}
}

func (j *supplierDiscoverJob) snapshot() *SupplierModelsDiscoverResult {
	result, _ := j.snapshotWithErr()
	return result
}

func (j *supplierDiscoverJob) snapshotWithErr() (*SupplierModelsDiscoverResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return cloneSupplierModelsDiscoverResult(j.result), j.err
}

func (j *supplierDiscoverJob) applyProbeOutcome(
	entry SupplierUpstreamModelEntry,
	probeResult SupplierProbeResult,
	defaultRatio *float64,
	authFailed bool,
) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.result == nil {
		return
	}
	if !j.noteProbeDoneLocked(entry.ID) {
		return
	}
	j.result.ProbeResults = append(j.result.ProbeResults, probeResult)
	if authFailed || probeResult.Status == SupplierProbeStatusAuthFailed {
		j.result.RejectedCandidates = append(j.result.RejectedCandidates, SupplierModelDiscoverRejection{
			UpstreamModelID: entry.ID, Type: entry.Type, Reason: string(probeResult.Status),
			Detail: probeResult.Detail,
		})
		j.result.FailedStep = "probe_candidate"
		j.result.ProbeStatus = SupplierDiscoverProbeFailed
		j.err = ErrSupplierSourceProbeFailed
		return
	}
	if probeResult.Status != SupplierProbeStatusPassed {
		j.result.RejectedCandidates = append(j.result.RejectedCandidates, SupplierModelDiscoverRejection{
			UpstreamModelID: entry.ID, Type: entry.Type, Reason: string(probeResult.Status),
			Detail: probeResult.Detail,
		})
		return
	}
	j.result.SuggestedAppends = append(j.result.SuggestedAppends, SupplierSourceModel{
		ClientModelID: entry.ID, UpstreamModelID: entry.ID, PurchaseRatio: cloneSupplierFloat64Ptr(defaultRatio),
	})
}

func (j *supplierDiscoverJob) markProbeSkipped(entry SupplierUpstreamModelEntry, reason string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.result == nil {
		return
	}
	if !j.noteProbeDoneLocked(entry.ID) {
		return
	}
	j.result.RejectedCandidates = append(j.result.RejectedCandidates, SupplierModelDiscoverRejection{
		UpstreamModelID: entry.ID, Type: entry.Type, Reason: reason,
	})
}

func (j *supplierDiscoverJob) noteProbeDoneLocked(upstreamModelID string) bool {
	if j.seen == nil {
		j.seen = make(map[string]struct{})
	}
	if _, exists := j.seen[upstreamModelID]; exists {
		return false
	}
	j.seen[upstreamModelID] = struct{}{}
	j.result.ProbeDone++
	if j.result.ProbeDone > j.result.ProbeTotal {
		j.result.ProbeDone = j.result.ProbeTotal
	}
	return true
}

func (j *supplierDiscoverJob) finish(authFailed bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.result == nil {
		return
	}
	if j.result.ProbeDone > j.result.ProbeTotal {
		j.result.ProbeDone = j.result.ProbeTotal
	}
	if authFailed || j.result.ProbeStatus == SupplierDiscoverProbeFailed {
		j.result.ProbeStatus = SupplierDiscoverProbeFailed
		if j.err == nil {
			j.err = ErrSupplierSourceProbeFailed
		}
		return
	}
	j.result.ProbeStatus = SupplierDiscoverProbeCompleted
	j.result.NeedsConfirmation = len(j.result.NormalizedChanges) > 0
}

func emptySupplierModelsDiscoverResult(sourceID int64) *SupplierModelsDiscoverResult {
	return &SupplierModelsDiscoverResult{
		SourceID:           sourceID,
		ProbeStatus:        SupplierDiscoverProbePending,
		UpstreamModels:     make([]SupplierUpstreamModelEntry, 0),
		NormalizedModels:   make([]SupplierSourceModel, 0),
		NormalizedChanges:  make([]SupplierModelNormalizeChange, 0),
		SuggestedAppends:   make([]SupplierSourceModel, 0),
		RejectedCandidates: make([]SupplierModelDiscoverRejection, 0),
		ConfiguredIssues:   make([]SupplierModelDiscoverIssue, 0),
		ProbeResults:       make([]SupplierProbeResult, 0),
	}
}

func cloneSupplierModelsDiscoverResult(in *SupplierModelsDiscoverResult) *SupplierModelsDiscoverResult {
	if in == nil {
		return emptySupplierModelsDiscoverResult(0)
	}
	out := *in
	out.UpstreamModels = append([]SupplierUpstreamModelEntry(nil), in.UpstreamModels...)
	out.NormalizedModels = cloneSupplierSourceModels(in.NormalizedModels)
	out.NormalizedChanges = append([]SupplierModelNormalizeChange(nil), in.NormalizedChanges...)
	out.SuggestedAppends = cloneSupplierSourceModels(in.SuggestedAppends)
	out.RejectedCandidates = append([]SupplierModelDiscoverRejection(nil), in.RejectedCandidates...)
	out.ConfiguredIssues = append([]SupplierModelDiscoverIssue(nil), in.ConfiguredIssues...)
	out.ProbeResults = append([]SupplierProbeResult(nil), in.ProbeResults...)
	return &out
}

func cloneSupplierSourceModels(in []SupplierSourceModel) []SupplierSourceModel {
	if in == nil {
		return make([]SupplierSourceModel, 0)
	}
	out := make([]SupplierSourceModel, len(in))
	for i, model := range in {
		out[i] = SupplierSourceModel{
			ClientModelID:   model.ClientModelID,
			UpstreamModelID: model.UpstreamModelID,
			PurchaseRatio:   cloneSupplierFloat64Ptr(model.PurchaseRatio),
		}
	}
	return out
}

func newSupplierDiscoverJobID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func supplierDiscoverProbeAccountID(sourceID int64) int64 {
	if sourceID <= 0 {
		return -1
	}
	return -sourceID
}

func cloneSupplierFloat64Ptr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func supplierConfiguredClientShouldNormalize(clientID, upstreamID, canonical string) bool {
	clientID = strings.TrimSpace(clientID)
	upstreamID = strings.TrimSpace(upstreamID)
	canonical = strings.TrimSpace(canonical)
	if clientID == "" || canonical == "" {
		return false
	}
	if clientID == upstreamID {
		return true
	}
	clientKey := supplierModelMatchKey(clientID)
	if clientKey == "" {
		return false
	}
	return clientKey == supplierModelMatchKey(upstreamID) || clientKey == supplierModelMatchKey(canonical)
}
