package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	supplierDiscoverDefaultPurchaseRatio = 1.0
	supplierDiscoverProbeConcurrency     = 4
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
// It never writes accounts or the supplier source row. The UI applies NormalizedModels when
// NeedsConfirmation is set; SuggestedAppends stay opt-in via an explicit form action.
type SupplierModelsDiscoverResult struct {
	SourceID           int64                            `json:"source_id"`
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

func (s *SupplierSourceService) DiscoverModels(ctx context.Context, sourceID int64) (*SupplierModelsDiscoverResult, error) {
	result := &SupplierModelsDiscoverResult{
		SourceID:           sourceID,
		UpstreamModels:     make([]SupplierUpstreamModelEntry, 0),
		NormalizedModels:   make([]SupplierSourceModel, 0),
		NormalizedChanges:  make([]SupplierModelNormalizeChange, 0),
		SuggestedAppends:   make([]SupplierSourceModel, 0),
		RejectedCandidates: make([]SupplierModelDiscoverRejection, 0),
		ConfiguredIssues:   make([]SupplierModelDiscoverIssue, 0),
		ProbeResults:       make([]SupplierProbeResult, 0),
	}
	if s == nil || s.repo == nil || s.probe == nil || s.encryptor == nil || sourceID <= 0 {
		result.FailedStep = "validate_request"
		return result, ErrSupplierSourceInvalidInput
	}
	lister, ok := s.probe.(SupplierUpstreamModelsLister)
	if !ok || lister == nil {
		result.FailedStep = "models_lister"
		return result, ErrSupplierSourceInvalidInput
	}
	source, err := s.repo.Get(ctx, sourceID)
	if err != nil {
		result.FailedStep = "load_source"
		return result, err
	}
	credential, err := s.encryptor.Decrypt(source.EncryptedCredential)
	if err != nil {
		result.FailedStep = "decrypt_credential"
		return result, fmt.Errorf("decrypt supplier credential: %w", err)
	}
	upstream, err := lister.ListSupplierUpstreamModels(ctx, source.Endpoint, credential)
	if err != nil {
		result.FailedStep = "list_upstream_models"
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
		// Preserve intentional client≠upstream remaps (e.g. FMGo Seedance). Only rewrite
		// client_model_id when it was an identity/fuzzy twin of the configured upstream id.
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
		return result, priorityErr
	}
	probeAccount := supplierProbeAccount(nil, source, credential, supplierTargetBand{
		Band: 6, Priority: priority, Mapping: map[string]string{},
	})
	defaultRatio := supplierDiscoverDefaultPurchaseRatio
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
	if len(probeable) == 0 {
		result.NeedsConfirmation = len(result.NormalizedChanges) > 0
		return result, nil
	}

	type probeOutcome struct {
		entry  SupplierUpstreamModelEntry
		result SupplierProbeResult
	}
	outcomes := make([]probeOutcome, len(probeable))
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var authFailed atomic.Bool
	sem := make(chan struct{}, supplierDiscoverProbeConcurrency)
	var wg sync.WaitGroup
	for index, entry := range probeable {
		wg.Add(1)
		go func(index int, entry SupplierUpstreamModelEntry) {
			defer wg.Done()
			if authFailed.Load() {
				return
			}
			select {
			case sem <- struct{}{}:
			case <-probeCtx.Done():
				return
			}
			defer func() { <-sem }()
			if authFailed.Load() {
				return
			}
			probeResult := s.probe.ProbeSupplierModel(probeCtx, SupplierProbeInput{
				Account: probeAccount, ClientModelID: entry.ID, UpstreamModelID: entry.ID,
			})
			probeResult.ClientModelID = entry.ID
			probeResult.UpstreamModelID = entry.ID
			outcomes[index] = probeOutcome{entry: entry, result: probeResult}
			if probeResult.Status == SupplierProbeStatusAuthFailed {
				authFailed.Store(true)
				cancel()
			}
		}(index, entry)
	}
	wg.Wait()

	for _, outcome := range outcomes {
		if outcome.entry.ID == "" && outcome.result.Status == "" {
			continue
		}
		entry := outcome.entry
		probeResult := outcome.result
		result.ProbeResults = append(result.ProbeResults, probeResult)
		if probeResult.Status == SupplierProbeStatusAuthFailed {
			result.RejectedCandidates = append(result.RejectedCandidates, SupplierModelDiscoverRejection{
				UpstreamModelID: entry.ID, Type: entry.Type, Reason: string(probeResult.Status),
				Detail: probeResult.Detail,
			})
			result.FailedStep = "probe_candidate"
			return result, ErrSupplierSourceProbeFailed
		}
		if probeResult.Status != SupplierProbeStatusPassed {
			result.RejectedCandidates = append(result.RejectedCandidates, SupplierModelDiscoverRejection{
				UpstreamModelID: entry.ID, Type: entry.Type, Reason: string(probeResult.Status),
				Detail: probeResult.Detail,
			})
			continue
		}
		result.SuggestedAppends = append(result.SuggestedAppends, SupplierSourceModel{
			ClientModelID: entry.ID, UpstreamModelID: entry.ID, PurchaseRatio: cloneSupplierFloat64Ptr(&defaultRatio),
		})
	}

	result.NeedsConfirmation = len(result.NormalizedChanges) > 0
	return result, nil
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
