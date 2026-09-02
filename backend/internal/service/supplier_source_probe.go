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

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

const (
	supplierProbeDefaultPurchaseRatio = 1.0
	// Concurrent live Chat probes for one supplier probe job. Admin-only, long-running.
	supplierProbeCandidateConcurrency = 8
	supplierProbeJobTTL               = 30 * time.Minute
	supplierProbeJobTimeout           = 15 * time.Minute
)

// SupplierProbeJobStatus is the async candidate-probe lifecycle for supplier probe.
type SupplierProbeJobStatus string

const (
	SupplierProbeJobPending   SupplierProbeJobStatus = "pending"
	SupplierProbeJobRunning   SupplierProbeJobStatus = "running"
	SupplierProbeJobCompleted SupplierProbeJobStatus = "completed"
	SupplierProbeJobFailed    SupplierProbeJobStatus = "failed"
)

// SupplierModelNormalizeChange records a configured-row rewrite to a canonical upstream id.
type SupplierModelNormalizeChange struct {
	FromClientModelID   string `json:"from_client_model_id"`
	FromUpstreamModelID string `json:"from_upstream_model_id"`
	ToClientModelID     string `json:"to_client_model_id"`
	ToUpstreamModelID   string `json:"to_upstream_model_id"`
}

// SupplierProbeConfiguredIssue is a configured model that cannot be matched to the upstream list.
type SupplierProbeConfiguredIssue struct {
	ClientModelID   string `json:"client_model_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	Reason          string `json:"reason"`
}

// SupplierProbeRejectedCandidate is an upstream-only candidate that was not suggested.
type SupplierProbeRejectedCandidate struct {
	UpstreamModelID string `json:"upstream_model_id"`
	Type            string `json:"type,omitempty"`
	Reason          string `json:"reason"`
	Detail          string `json:"detail,omitempty"`
}

// SupplierSourceProbeResult is a read-only preview used by probe before account projection.
// List + normalize return immediately; candidate Chat probes continue asynchronously under JobID.
// It never writes accounts or the supplier source row. The UI applies NormalizedModels when
// NeedsConfirmation is set; SuggestedAppends stay opt-in via an explicit form action.
type SupplierSourceProbeResult struct {
	SourceID           int64                            `json:"source_id"`
	JobID              string                           `json:"job_id,omitempty"`
	ProbeStatus        SupplierProbeJobStatus           `json:"probe_status"`
	ProbeTotal         int                              `json:"probe_total"`
	ProbeDone          int                              `json:"probe_done"`
	UpstreamModels     []SupplierUpstreamModelEntry     `json:"upstream_models"`
	NormalizedModels   []SupplierSourceModel            `json:"normalized_models"`
	NormalizedChanges  []SupplierModelNormalizeChange   `json:"normalized_changes"`
	SuggestedAppends   []SupplierSourceModel            `json:"suggested_appends"`
	RejectedCandidates []SupplierProbeRejectedCandidate `json:"rejected_candidates"`
	ConfiguredIssues   []SupplierProbeConfiguredIssue   `json:"configured_issues"`
	ProbeResults       []SupplierProbeResult            `json:"probe_results"`
	NeedsConfirmation  bool                             `json:"needs_confirmation"`
	FailedStep         string                           `json:"failed_step,omitempty"`
}

type supplierProbeJob struct {
	cancel    context.CancelFunc
	mu        sync.Mutex
	result    *SupplierSourceProbeResult
	err       error
	seen      map[string]struct{}
	createdAt time.Time
}

type supplierProbeJobRegistry struct {
	mu       sync.Mutex
	byID     map[string]*supplierProbeJob
	bySource map[int64]string
}

func newSupplierProbeJobRegistry() *supplierProbeJobRegistry {
	return &supplierProbeJobRegistry{
		byID:     make(map[string]*supplierProbeJob),
		bySource: make(map[int64]string),
	}
}

func (s *SupplierSourceService) probeJobRegistry() *supplierProbeJobRegistry {
	if s == nil {
		return nil
	}
	if s.probeJobs == nil {
		s.probeJobs = newSupplierProbeJobRegistry()
	}
	return s.probeJobs
}

func (s *SupplierSourceService) probeConfiguredSourceModels(ctx context.Context, sourceID int64) ([]SupplierProbeResult, error) {
	if s == nil || s.repo == nil || s.probe == nil || s.encryptor == nil {
		return nil, ErrSupplierSourceInvalidInput
	}
	source, err := s.repo.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if len(source.Models) == 0 {
		return []SupplierProbeResult{}, nil
	}
	credential, err := s.encryptor.Decrypt(source.EncryptedCredential)
	if err != nil {
		return nil, fmt.Errorf("decrypt supplier credential: %w", err)
	}
	targets, err := supplierTargetBands(source)
	if err != nil {
		return nil, err
	}
	results := s.probeSupplierTargets(ctx, source, credential, targets, nil, nil)
	if supplierProbeResultsFailed(results) {
		return results, ErrSupplierSourceProbeFailed
	}
	return results, nil
}

// StartSupplierProbeJob lists/normalizes synchronously, then probes every probeable upstream
// candidate asynchronously with high concurrency. Poll GetSupplierProbeJob until ProbeStatus
// is completed or failed. SuggestedAppends only include probe-passed models.
func (s *SupplierSourceService) StartSupplierProbeJob(ctx context.Context, sourceID int64) (*SupplierSourceProbeResult, error) {
	result := emptySupplierSourceProbeResult(sourceID)
	if s == nil || s.repo == nil || s.probe == nil || s.encryptor == nil || sourceID <= 0 {
		result.FailedStep = "validate_request"
		result.ProbeStatus = SupplierProbeJobFailed
		return result, ErrSupplierSourceInvalidInput
	}
	lister, ok := s.probe.(SupplierUpstreamModelsLister)
	if !ok || lister == nil {
		result.FailedStep = "models_lister"
		result.ProbeStatus = SupplierProbeJobFailed
		return result, ErrSupplierSourceInvalidInput
	}
	source, err := s.repo.Get(ctx, sourceID)
	if err != nil {
		result.FailedStep = "load_source"
		result.ProbeStatus = SupplierProbeJobFailed
		return result, err
	}
	credential, err := s.encryptor.Decrypt(source.EncryptedCredential)
	if err != nil {
		result.FailedStep = "decrypt_credential"
		result.ProbeStatus = SupplierProbeJobFailed
		return result, fmt.Errorf("decrypt supplier credential: %w", err)
	}
	upstream, err := lister.ListSupplierUpstreamModels(ctx, source.Endpoint, source.ChannelType, credential)
	if err != nil {
		result.FailedStep = "list_upstream_models"
		result.ProbeStatus = SupplierProbeJobFailed
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
			result.ConfiguredIssues = append(result.ConfiguredIssues, SupplierProbeConfiguredIssue{
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
		result.ProbeStatus = SupplierProbeJobFailed
		return result, priorityErr
	}
	probeAccount := supplierProbeAccount(nil, source, credential, supplierTargetBand{
		Band: 6, Priority: priority, Mapping: map[string]string{},
	})
	probeAccount.ID = supplierProbeAccountID(source.ID)
	probeAccount.Concurrency = supplierProbeCandidateConcurrency

	probeable := make([]SupplierUpstreamModelEntry, 0, len(candidates))
	for _, entry := range candidates {
		if !supplierUpstreamTypeProbeable(entry.Type, source.ChannelType) {
			result.RejectedCandidates = append(result.RejectedCandidates, SupplierProbeRejectedCandidate{
				UpstreamModelID: entry.ID, Type: entry.Type, Reason: "non_chat_type",
			})
			continue
		}
		if newapiintegration.IsFMGoBaseURL(source.ChannelType, source.Endpoint) &&
			!newapiintegration.IsFMGoVideoInventoryID(entry.ID) {
			result.RejectedCandidates = append(result.RejectedCandidates, SupplierProbeRejectedCandidate{
				UpstreamModelID: entry.ID, Type: entry.Type, Reason: "non_video_inventory",
			})
			continue
		}
		probeable = append(probeable, entry)
	}

	result.NeedsConfirmation = len(result.NormalizedChanges) > 0
	result.ProbeTotal = len(probeable)
	if len(probeable) == 0 {
		result.ProbeStatus = SupplierProbeJobCompleted
		result.ProbeDone = 0
		return cloneSupplierSourceProbeResult(result), nil
	}

	jobID, err := newSupplierProbeJobID()
	if err != nil {
		result.FailedStep = "create_job"
		result.ProbeStatus = SupplierProbeJobFailed
		return result, fmt.Errorf("create probe job id: %w", err)
	}
	result.JobID = jobID
	result.ProbeStatus = SupplierProbeJobRunning
	result.ProbeDone = 0

	jobCtx, cancel := context.WithTimeout(context.Background(), supplierProbeJobTimeout)
	job := &supplierProbeJob{
		cancel:    cancel,
		result:    cloneSupplierSourceProbeResult(result),
		createdAt: time.Now().UTC(),
	}
	s.probeJobRegistry().put(sourceID, jobID, job)
	go s.runSupplierProbeCandidates(jobCtx, job, probeAccount, probeable)
	return cloneSupplierSourceProbeResult(job.snapshot()), nil
}

// ProbeUntilComplete starts async probe and, for callers that expect a finished snapshot
// (unit tests), waits until the probe job completes. HTTP handlers should use
// StartSupplierProbeJob + GetSupplierProbeJob instead.
func (s *SupplierSourceService) ProbeUntilComplete(ctx context.Context, sourceID int64) (*SupplierSourceProbeResult, error) {
	result, err := s.StartSupplierProbeJob(ctx, sourceID)
	if err != nil {
		return result, err
	}
	if result == nil || result.JobID == "" || result.ProbeStatus != SupplierProbeJobRunning {
		return result, nil
	}
	deadline := time.Now().Add(supplierProbeJobTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		next, getErr := s.GetSupplierProbeJob(ctx, sourceID, result.JobID)
		if getErr != nil {
			return next, getErr
		}
		result = next
		if result.ProbeStatus == SupplierProbeJobCompleted {
			return result, nil
		}
		if result.ProbeStatus == SupplierProbeJobFailed {
			if result.FailedStep == "probe_candidate" {
				return result, ErrSupplierSourceProbeFailed
			}
			return result, fmt.Errorf("supplier source probe failed")
		}
	}
	result.FailedStep = "probe_timeout"
	result.ProbeStatus = SupplierProbeJobFailed
	return result, fmt.Errorf("supplier source probe timed out")
}

// GetSupplierProbeJob returns the latest snapshot for an async supplier probe job.
func (s *SupplierSourceService) GetSupplierProbeJob(ctx context.Context, sourceID int64, jobID string) (*SupplierSourceProbeResult, error) {
	_ = ctx
	if s == nil || sourceID <= 0 || strings.TrimSpace(jobID) == "" {
		return emptySupplierSourceProbeResult(sourceID), ErrSupplierSourceInvalidInput
	}
	job, ok := s.probeJobRegistry().get(sourceID, jobID)
	if !ok || job == nil {
		// Poll-friendly: expired/superseded jobs are not "source missing".
		result := emptySupplierSourceProbeResult(sourceID)
		result.JobID = jobID
		result.FailedStep = "job_not_found"
		result.ProbeStatus = SupplierProbeJobFailed
		return result, nil
	}
	result := job.snapshot()
	return result, nil
}

func (s *SupplierSourceService) runSupplierProbeCandidates(
	ctx context.Context,
	job *supplierProbeJob,
	probeAccount *Account,
	probeable []SupplierUpstreamModelEntry,
) {
	defer job.cancel()
	defaultRatio := supplierProbeDefaultPurchaseRatio
	var authFailed atomic.Bool
	sem := make(chan struct{}, supplierProbeCandidateConcurrency)
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

func (r *supplierProbeJobRegistry) put(sourceID int64, jobID string, job *supplierProbeJob) {
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

func (r *supplierProbeJobRegistry) get(sourceID int64, jobID string) (*supplierProbeJob, bool) {
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

func (r *supplierProbeJobRegistry) evictExpiredLocked(now time.Time) {
	for id, job := range r.byID {
		if job == nil || now.Sub(job.createdAt) <= supplierProbeJobTTL {
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

func (j *supplierProbeJob) snapshot() *SupplierSourceProbeResult {
	result, _ := j.snapshotWithErr()
	return result
}

func (j *supplierProbeJob) snapshotWithErr() (*SupplierSourceProbeResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return cloneSupplierSourceProbeResult(j.result), j.err
}

func (j *supplierProbeJob) applyProbeOutcome(
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
		j.result.RejectedCandidates = append(j.result.RejectedCandidates, SupplierProbeRejectedCandidate{
			UpstreamModelID: entry.ID, Type: entry.Type, Reason: string(probeResult.Status),
			Detail: probeResult.Detail,
		})
		j.result.FailedStep = "probe_candidate"
		j.result.ProbeStatus = SupplierProbeJobFailed
		j.err = ErrSupplierSourceProbeFailed
		return
	}
	if probeResult.Status != SupplierProbeStatusPassed {
		j.result.RejectedCandidates = append(j.result.RejectedCandidates, SupplierProbeRejectedCandidate{
			UpstreamModelID: entry.ID, Type: entry.Type, Reason: string(probeResult.Status),
			Detail: probeResult.Detail,
		})
		return
	}
	j.result.SuggestedAppends = append(j.result.SuggestedAppends, SupplierSourceModel{
		ClientModelID: entry.ID, UpstreamModelID: entry.ID, PurchaseRatio: cloneSupplierFloat64Ptr(defaultRatio),
	})
}

func (j *supplierProbeJob) markProbeSkipped(entry SupplierUpstreamModelEntry, reason string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.result == nil {
		return
	}
	if !j.noteProbeDoneLocked(entry.ID) {
		return
	}
	j.result.RejectedCandidates = append(j.result.RejectedCandidates, SupplierProbeRejectedCandidate{
		UpstreamModelID: entry.ID, Type: entry.Type, Reason: reason,
	})
}

func (j *supplierProbeJob) noteProbeDoneLocked(upstreamModelID string) bool {
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

func (j *supplierProbeJob) finish(authFailed bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.result == nil {
		return
	}
	if j.result.ProbeDone > j.result.ProbeTotal {
		j.result.ProbeDone = j.result.ProbeTotal
	}
	if authFailed || j.result.ProbeStatus == SupplierProbeJobFailed {
		j.result.ProbeStatus = SupplierProbeJobFailed
		if j.err == nil {
			j.err = ErrSupplierSourceProbeFailed
		}
		return
	}
	j.result.ProbeStatus = SupplierProbeJobCompleted
	j.result.NeedsConfirmation = len(j.result.NormalizedChanges) > 0
}

func emptySupplierSourceProbeResult(sourceID int64) *SupplierSourceProbeResult {
	return &SupplierSourceProbeResult{
		SourceID:           sourceID,
		ProbeStatus:        SupplierProbeJobPending,
		UpstreamModels:     make([]SupplierUpstreamModelEntry, 0),
		NormalizedModels:   make([]SupplierSourceModel, 0),
		NormalizedChanges:  make([]SupplierModelNormalizeChange, 0),
		SuggestedAppends:   make([]SupplierSourceModel, 0),
		RejectedCandidates: make([]SupplierProbeRejectedCandidate, 0),
		ConfiguredIssues:   make([]SupplierProbeConfiguredIssue, 0),
		ProbeResults:       make([]SupplierProbeResult, 0),
	}
}

func cloneSupplierSourceProbeResult(in *SupplierSourceProbeResult) *SupplierSourceProbeResult {
	if in == nil {
		return emptySupplierSourceProbeResult(0)
	}
	out := *in
	out.UpstreamModels = append([]SupplierUpstreamModelEntry(nil), in.UpstreamModels...)
	out.NormalizedModels = cloneSupplierSourceModels(in.NormalizedModels)
	out.NormalizedChanges = append([]SupplierModelNormalizeChange(nil), in.NormalizedChanges...)
	out.SuggestedAppends = cloneSupplierSourceModels(in.SuggestedAppends)
	out.RejectedCandidates = append([]SupplierProbeRejectedCandidate(nil), in.RejectedCandidates...)
	out.ConfiguredIssues = append([]SupplierProbeConfiguredIssue(nil), in.ConfiguredIssues...)
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

func newSupplierProbeJobID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func supplierProbeAccountID(sourceID int64) int64 {
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
