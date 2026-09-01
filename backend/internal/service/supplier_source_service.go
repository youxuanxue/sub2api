package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

type SupplierPriorityPreviewEntry struct {
	SourceID         int64    `json:"source_id"`
	SupplierName     string   `json:"supplier_name"`
	ChannelName      string   `json:"channel_name"`
	DiscountBand     int      `json:"discount_band"`
	DiscountPriority int      `json:"discount_priority"`
	Priority         int      `json:"priority"`
	ClientModelIDs   []string `json:"client_model_ids"`
}

type SupplierPriorityPreviewWarning struct {
	Code      string  `json:"code"`
	Priority  int     `json:"priority"`
	SourceIDs []int64 `json:"source_ids"`
}

type SupplierPriorityPreview struct {
	Entries  []SupplierPriorityPreviewEntry   `json:"entries"`
	Warnings []SupplierPriorityPreviewWarning `json:"warnings"`
}

type SupplierProbeInput struct {
	Account         *Account
	ClientModelID   string
	UpstreamModelID string
}

type SupplierSourceAccountChange struct {
	AccountID         int64    `json:"account_id"`
	DiscountBand      int      `json:"discount_band"`
	Action            string   `json:"action"`
	AddedModels       []string `json:"added_models"`
	RemovedModels     []string `json:"removed_models"`
	PriorityBefore    *int     `json:"priority_before,omitempty"`
	PriorityAfter     int      `json:"priority_after"`
	SchedulableBefore *bool    `json:"schedulable_before,omitempty"`
	SchedulableAfter  bool     `json:"schedulable_after"`
}

type SupplierSourceSyncResult struct {
	SourceID     int64                         `json:"source_id"`
	ProbeResults []SupplierProbeResult         `json:"probe_results"`
	Changes      []SupplierSourceAccountChange `json:"changes"`
	FailedStep   string                        `json:"failed_step,omitempty"`
}

type SupplierAccountMatch struct {
	Endpoint              string
	CredentialFingerprint string
}

type SupplierSourceAccountStore interface {
	ListManagedAccounts(ctx context.Context, sourceID int64) ([]*Account, error)
	FindCredentialEndpointMatches(ctx context.Context, match SupplierAccountMatch) ([]*Account, error)
	CreateManagedAccount(ctx context.Context, input SupplierManagedAccountCreateInput) (*Account, error)
	UpdateManagedAccount(ctx context.Context, input SupplierManagedAccountUpdateInput) (*Account, error)
	UpdateManagedAccountConcurrency(ctx context.Context, accountID, sourceID int64, discountBand, concurrency int) (*Account, error)
	GetAccount(ctx context.Context, accountID int64) (*Account, error)
}

type SupplierSourceProbe interface {
	ProbeSupplierModel(ctx context.Context, input SupplierProbeInput) SupplierProbeResult
}

type SupplierCredentialFingerprinter interface {
	Fingerprint(credential string) (string, error)
}

type SupplierSourceService struct {
	repo          SupplierSourceRepository
	accounts      SupplierSourceAccountStore
	probe         SupplierSourceProbe
	encryptor     SecretEncryptor
	fingerprinter SupplierCredentialFingerprinter
	probeJobs     *supplierProbeJobRegistry
}

func NewSupplierSourceService(
	repo SupplierSourceRepository,
	accounts SupplierSourceAccountStore,
	probe SupplierSourceProbe,
	encryptor SecretEncryptor,
	fingerprinter SupplierCredentialFingerprinter,
) *SupplierSourceService {
	return &SupplierSourceService{
		repo: repo, accounts: accounts, probe: probe, encryptor: encryptor, fingerprinter: fingerprinter,
		probeJobs: newSupplierProbeJobRegistry(),
	}
}

func (s *SupplierSourceService) List(ctx context.Context) ([]SupplierSource, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSupplierSourceInvalidInput
	}
	return s.repo.List(ctx)
}

func (s *SupplierSourceService) Get(ctx context.Context, id int64) (*SupplierSource, error) {
	if s == nil || s.repo == nil || id <= 0 {
		return nil, ErrSupplierSourceInvalidInput
	}
	return s.repo.Get(ctx, id)
}

func (s *SupplierSourceService) Create(ctx context.Context, input SupplierSourceInput) (*SupplierSource, error) {
	if s == nil || s.repo == nil || s.encryptor == nil || s.fingerprinter == nil {
		return nil, ErrSupplierSourceInvalidInput
	}
	if strings.TrimSpace(input.Credential) == "" {
		return nil, fmt.Errorf("%w: credential is required", ErrSupplierSourceInvalidInput)
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if err := input.Normalize(); err != nil {
		return nil, err
	}

	encryptedCredential, fingerprint, err := s.protectCredential(input.Credential)
	if err != nil {
		return nil, err
	}
	basePriority := 100
	if input.BasePriority != nil {
		basePriority = *input.BasePriority
	}
	accountConcurrency := SupplierSourceDefaultAccountConcurrency
	if input.AccountConcurrency != nil {
		accountConcurrency = ResolveSupplierSourceAccountConcurrency(*input.AccountConcurrency)
	}
	source := supplierSourceFromInput(input, basePriority, accountConcurrency)
	source.EncryptedCredential = encryptedCredential
	source.CredentialFingerprint = fingerprint
	if err := s.repo.Create(ctx, source); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *SupplierSourceService) Update(ctx context.Context, id int64, input SupplierSourceInput) (*SupplierSource, error) {
	if s == nil || s.repo == nil || s.encryptor == nil || s.fingerprinter == nil || id <= 0 {
		return nil, ErrSupplierSourceInvalidInput
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if err := input.Normalize(); err != nil {
		return nil, err
	}
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	basePriority := existing.BasePriority
	if input.BasePriority != nil {
		basePriority = *input.BasePriority
	}
	accountConcurrency := existing.AccountConcurrency
	if input.AccountConcurrency != nil {
		accountConcurrency = ResolveSupplierSourceAccountConcurrency(*input.AccountConcurrency)
	}
	updated := supplierSourceFromInput(input, basePriority, accountConcurrency)
	updated.ID = existing.ID
	updated.CreatedAt = existing.CreatedAt
	updated.EncryptedCredential = existing.EncryptedCredential
	updated.CredentialFingerprint = existing.CredentialFingerprint
	if strings.TrimSpace(input.Credential) != "" {
		updated.EncryptedCredential, updated.CredentialFingerprint, err = s.protectCredential(input.Credential)
		if err != nil {
			return nil, err
		}
	}
	if err := s.repo.Update(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *SupplierSourceService) PriorityPreview(ctx context.Context) (*SupplierPriorityPreview, error) {
	sources, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	preview := &SupplierPriorityPreview{
		Entries: make([]SupplierPriorityPreviewEntry, 0), Warnings: make([]SupplierPriorityPreviewWarning, 0),
	}
	for _, source := range sources {
		targets, targetErr := supplierTargetBands(&source)
		if targetErr != nil {
			return nil, targetErr
		}
		for _, band := range sortedSupplierBands(targets) {
			modelIDs := sortedSupplierMappingKeys(targets[band].Mapping)
			discountPriority, priorityErr := SupplierDiscountPriority(band)
			if priorityErr != nil {
				return nil, priorityErr
			}
			preview.Entries = append(preview.Entries, SupplierPriorityPreviewEntry{
				SourceID: source.ID, SupplierName: source.SupplierName, ChannelName: source.ChannelName,
				DiscountBand: band, DiscountPriority: discountPriority, Priority: targets[band].Priority,
				ClientModelIDs: modelIDs,
			})
		}
	}
	sort.Slice(preview.Entries, func(i, j int) bool {
		if preview.Entries[i].Priority != preview.Entries[j].Priority {
			return preview.Entries[i].Priority < preview.Entries[j].Priority
		}
		if preview.Entries[i].SourceID != preview.Entries[j].SourceID {
			return preview.Entries[i].SourceID < preview.Entries[j].SourceID
		}
		return preview.Entries[i].DiscountBand < preview.Entries[j].DiscountBand
	})
	preview.Warnings = supplierPriorityOverlapWarnings(preview.Entries)
	return preview, nil
}

func (s *SupplierSourceService) Sync(ctx context.Context, sourceID int64) (*SupplierSourceSyncResult, error) {
	result := &SupplierSourceSyncResult{
		SourceID: sourceID, ProbeResults: make([]SupplierProbeResult, 0), Changes: make([]SupplierSourceAccountChange, 0),
	}
	if s == nil || s.repo == nil || s.accounts == nil || s.probe == nil || s.encryptor == nil || sourceID <= 0 {
		result.FailedStep = "validate_request"
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
	targets, err := supplierTargetBands(source)
	if err != nil {
		result.FailedStep = "build_targets"
		return result, err
	}
	managed, err := s.accounts.ListManagedAccounts(ctx, source.ID)
	if err != nil {
		result.FailedStep = "load_managed_accounts"
		return result, err
	}
	managedByBand, err := supplierAccountsByBand(managed)
	if err != nil {
		result.FailedStep = "index_managed_accounts"
		return result, err
	}

	adoptByBand, err := s.findReusableAccounts(ctx, source, credential, targets, managed)
	if err != nil {
		result.FailedStep = "match_existing_account"
		return result, err
	}
	structureChanged := supplierStructureChanged(source, credential, targets, managedByBand)
	if len(adoptByBand) > 0 {
		structureChanged = true
	}
	if !structureChanged {
		if err := s.syncSupplierMetadata(ctx, source, managedByBand, result); err != nil {
			return result, err
		}
		workingByBand := make(map[int]*Account, len(managedByBand))
		for band, account := range managedByBand {
			workingByBand[band] = cloneSupplierProjectionAccount(account)
		}
		if err := s.ensureSupplierManagedConcurrency(ctx, source, targets, workingByBand, result); err != nil {
			return result, err
		}
		return result, nil
	}

	if len(source.Models) > 0 {
		result.ProbeResults = s.probeSupplierTargets(ctx, source, credential, targets, managedByBand, adoptByBand)
		if supplierProbeResultsFailed(result.ProbeResults) {
			result.FailedStep = "probe"
			return result, ErrSupplierSourceProbeFailed
		}
	}

	workingByBand := make(map[int]*Account, len(managedByBand)+len(targets))
	for band, account := range managedByBand {
		workingByBand[band] = cloneSupplierProjectionAccount(account)
	}
	allTargetMapping := supplierAllTargetMapping(targets)
	for _, band := range sortedSupplierBands(targets) {
		target := targets[band]
		account := workingByBand[band]
		before := cloneSupplierProjectionAccount(account)
		adopt := false
		created := false
		if account == nil {
			if reusable := adoptByBand[band]; reusable != nil {
				account = reusable
				before = cloneSupplierProjectionAccount(reusable)
				adopt = true
			} else {
				createInput := SupplierManagedAccountCreateInput{
					SourceID: source.ID, DiscountBand: band, Name: supplierManagedAccountName(source, band),
					Endpoint: source.Endpoint, ChannelType: source.ChannelType, Credential: credential,
					Priority: target.Priority, Concurrency: source.AccountConcurrency,
				}
				account, err = s.accounts.CreateManagedAccount(ctx, createInput)
				if err != nil {
					result.FailedStep = fmt.Sprintf("create_band_%d", band)
					return result, err
				}
				created = true
				workingByBand[band] = account
			}
		}

		additionMapping := supplierAdditionMapping(account, target.Mapping, allTargetMapping)
		updateInput := SupplierManagedAccountUpdateInput{
			AccountID: account.ID, SourceID: source.ID, DiscountBand: band,
			Name: supplierManagedAccountName(source, band), Endpoint: source.Endpoint, ChannelType: source.ChannelType,
			Credential:   credential,
			ModelMapping: additionMapping, Priority: target.Priority, Concurrency: source.AccountConcurrency,
			Status:      StatusActive,
			Schedulable: len(additionMapping) > 0, Adopt: adopt,
			ChatProbePassed: len(additionMapping) > 0,
		}
		updated, updateErr := s.accounts.UpdateManagedAccount(ctx, updateInput)
		if updateErr != nil {
			if created {
				result.Changes = append(result.Changes, supplierAccountChange(before, account, band, "created"))
			}
			result.FailedStep = fmt.Sprintf("add_band_%d", band)
			return result, updateErr
		}
		action := "updated"
		if created {
			action = "created"
		} else if adopt {
			action = "adopted"
		}
		readback, readbackErr := s.accounts.GetAccount(ctx, updated.ID)
		if readbackErr != nil {
			result.Changes = append(result.Changes, supplierAccountChange(before, updated, band, action))
			result.FailedStep = fmt.Sprintf("verify_band_%d", band)
			return result, readbackErr
		}
		workingByBand[band] = readback
		if verifyErr := verifySupplierTargetPresent(readback, source.ID, band, target); verifyErr != nil {
			result.Changes = append(result.Changes, supplierAccountChange(before, readback, band, action))
			result.FailedStep = fmt.Sprintf("verify_band_%d", band)
			return result, verifyErr
		}
		result.Changes = append(result.Changes, supplierAccountChange(before, readback, band, action))
	}

	for _, band := range sortedSupplierAccountBands(workingByBand) {
		account := workingByBand[band]
		desiredMapping := map[string]string{}
		desiredPriority, priorityErr := SupplierAccountPriority(source.BasePriority, band)
		if priorityErr != nil {
			result.FailedStep = fmt.Sprintf("remove_band_%d", band)
			return result, priorityErr
		}
		if target, exists := targets[band]; exists {
			desiredMapping = target.Mapping
			desiredPriority = target.Priority
		}
		if supplierAccountMatchesDesired(account, source, credential, desiredMapping, desiredPriority) {
			continue
		}
		before := cloneSupplierProjectionAccount(account)
		updated, updateErr := s.accounts.UpdateManagedAccount(ctx, SupplierManagedAccountUpdateInput{
			AccountID: account.ID, SourceID: source.ID, DiscountBand: band,
			Name: supplierManagedAccountName(source, band), Endpoint: source.Endpoint, ChannelType: source.ChannelType,
			Credential:   credential,
			ModelMapping: cloneSupplierStringMap(desiredMapping), Priority: desiredPriority,
			Concurrency: source.AccountConcurrency,
			Status:      StatusActive, Schedulable: len(desiredMapping) > 0,
			ChatProbePassed: len(desiredMapping) > 0,
		})
		if updateErr != nil {
			result.FailedStep = fmt.Sprintf("remove_band_%d", band)
			return result, updateErr
		}
		workingByBand[band] = updated
		action := "updated"
		if len(desiredMapping) == 0 {
			action = "cleared"
		}
		result.Changes = append(result.Changes, supplierAccountChange(before, updated, band, action))
	}
	return result, nil
}

type supplierTargetBand struct {
	Band     int
	Priority int
	Mapping  map[string]string
}

func supplierTargetBands(source *SupplierSource) (map[int]supplierTargetBand, error) {
	targets := make(map[int]supplierTargetBand)
	if source == nil {
		return targets, ErrSupplierSourceInvalidInput
	}
	for _, model := range source.Models {
		band, err := SupplierDiscountBandForRatio(model.PurchaseRatio)
		if err != nil {
			return nil, err
		}
		target := targets[band]
		if target.Mapping == nil {
			priority, priorityErr := SupplierAccountPriority(source.BasePriority, band)
			if priorityErr != nil {
				return nil, priorityErr
			}
			target = supplierTargetBand{Band: band, Priority: priority, Mapping: make(map[string]string)}
		}
		target.Mapping[model.ClientModelID] = model.UpstreamModelID
		targets[band] = target
	}
	return targets, nil
}

func (s *SupplierSourceService) findReusableAccounts(
	ctx context.Context,
	source *SupplierSource,
	credential string,
	targets map[int]supplierTargetBand,
	managed []*Account,
) (map[int]*Account, error) {
	result := make(map[int]*Account)
	managedBands := make(map[int]struct{}, len(managed))
	for _, account := range managed {
		if band, ok := supplierDiscountBandFromAccount(account); ok {
			managedBands[band] = struct{}{}
		}
	}
	missingTarget := false
	for band := range targets {
		if _, exists := managedBands[band]; !exists {
			missingTarget = true
			break
		}
	}
	if !missingTarget {
		return result, nil
	}
	matches, err := s.accounts.FindCredentialEndpointMatches(ctx, SupplierAccountMatch{
		Endpoint: source.Endpoint, CredentialFingerprint: source.CredentialFingerprint,
	})
	if err != nil {
		return nil, err
	}
	externalMatches := make([]*Account, 0, len(matches))
	for _, match := range matches {
		managedSourceID, managedMatch := supplierSourceIDFromAccount(match)
		if managedMatch && managedSourceID == source.ID {
			continue
		}
		externalMatches = append(externalMatches, match)
	}
	if len(externalMatches) == 0 {
		return result, nil
	}
	if len(externalMatches) > 1 {
		return nil, ErrSupplierSourceMultipleMatches
	}
	match := externalMatches[0]
	if len(managed) != 0 || len(targets) != 1 || IsSupplierManagedAccount(match) ||
		match.Status != StatusActive || !supplierReusableAccountTransport(match, source.Endpoint, source.ChannelType) {
		return nil, ErrSupplierSourceIdentityConflict
	}
	band := sortedSupplierBands(targets)[0]
	if !supplierMappingIsSubsetOfTarget(supplierModelMapping(match.Credentials), targets[band].Mapping) {
		return nil, ErrSupplierSourceIdentityConflict
	}
	baseURL, _ := match.Credentials["base_url"].(string)
	if !supplierManagedEndpointsEqual(baseURL, source.Endpoint) {
		return nil, ErrSupplierSourceIdentityConflict
	}
	apiKey, _ := match.Credentials["api_key"].(string)
	if strings.TrimSpace(apiKey) != strings.TrimSpace(credential) {
		return nil, ErrSupplierSourceIdentityConflict
	}
	result[band] = cloneSupplierProjectionAccount(match)
	return result, nil
}

func (s *SupplierSourceService) probeSupplierTargets(
	ctx context.Context,
	source *SupplierSource,
	credential string,
	targets map[int]supplierTargetBand,
	managedByBand map[int]*Account,
	adoptByBand map[int]*Account,
) []SupplierProbeResult {
	results := make([]SupplierProbeResult, 0, len(source.Models))
	for _, model := range source.Models {
		band, _ := SupplierDiscountBandForRatio(model.PurchaseRatio)
		baseAccount := managedByBand[band]
		if baseAccount == nil {
			baseAccount = adoptByBand[band]
		}
		probeAccount := supplierProbeAccount(baseAccount, source, credential, targets[band])
		result := s.probe.ProbeSupplierModel(ctx, SupplierProbeInput{
			Account: probeAccount, ClientModelID: model.ClientModelID, UpstreamModelID: model.UpstreamModelID,
		})
		result.ClientModelID = model.ClientModelID
		result.UpstreamModelID = model.UpstreamModelID
		results = append(results, result)
	}
	return results
}

func (s *SupplierSourceService) syncSupplierMetadata(
	ctx context.Context,
	source *SupplierSource,
	managedByBand map[int]*Account,
	result *SupplierSourceSyncResult,
) error {
	for _, band := range sortedSupplierAccountBands(managedByBand) {
		account := managedByBand[band]
		name := supplierManagedAccountName(source, band)
		priority, err := SupplierAccountPriority(source.BasePriority, band)
		if err != nil {
			result.FailedStep = fmt.Sprintf("metadata_band_%d", band)
			return err
		}
		if account.Name == name && account.Priority == priority {
			continue
		}
		before := cloneSupplierProjectionAccount(account)
		updated, updateErr := s.accounts.UpdateManagedAccount(ctx, SupplierManagedAccountUpdateInput{
			AccountID: account.ID, SourceID: source.ID, DiscountBand: band,
			Name: name, Priority: priority, MetadataOnly: true,
		})
		if updateErr != nil {
			result.FailedStep = fmt.Sprintf("metadata_band_%d", band)
			return updateErr
		}
		result.Changes = append(result.Changes, supplierAccountChange(before, updated, band, "updated"))
	}
	return nil
}

func supplierStructureChanged(
	source *SupplierSource,
	credential string,
	targets map[int]supplierTargetBand,
	managedByBand map[int]*Account,
) bool {
	for band, target := range targets {
		account := managedByBand[band]
		if account == nil || !supplierAccountStructureMatches(account, source.Endpoint, source.ChannelType, credential, target.Mapping) ||
			supplierAccountNeedsProtocolRepublish(account, source.Endpoint, source.ChannelType) ||
			!supplierSchedulingProjectionMatches(account, target.Mapping) {
			return true
		}
	}
	for band, account := range managedByBand {
		target, exists := targets[band]
		mapping := map[string]string{}
		if exists {
			mapping = target.Mapping
		}
		if !supplierAccountStructureMatches(account, source.Endpoint, source.ChannelType, credential, mapping) ||
			supplierAccountNeedsProtocolRepublish(account, source.Endpoint, source.ChannelType) ||
			!supplierSchedulingProjectionMatches(account, mapping) {
			return true
		}
	}
	return false
}

func supplierAccountStructureMatches(account *Account, endpoint string, channelType int, credential string, mapping map[string]string) bool {
	if !supplierReusableAccountTransport(account, endpoint, channelType) {
		return false
	}
	baseURL, _ := account.Credentials["base_url"].(string)
	if !supplierManagedEndpointsEqual(baseURL, endpoint) {
		return false
	}
	apiKey, _ := account.Credentials["api_key"].(string)
	if strings.TrimSpace(apiKey) != strings.TrimSpace(credential) {
		return false
	}
	return supplierStringMapsEqual(supplierModelMapping(account.Credentials), mapping)
}

func supplierAccountNeedsProtocolRepublish(account *Account, sourceEndpoint string, sourceChannelType int) bool {
	if account == nil || len(supplierModelMapping(account.Credentials)) == 0 {
		return false
	}
	// Migrate legacy managed credentials that still fan identity from bare base_url.
	if !accountDeclaresExclusiveProtocolEndpoints(account) {
		return true
	}
	want, err := resolveSupplierManagedTransport(sourceEndpoint, sourceChannelType)
	if err != nil {
		return true
	}
	if account.ChannelType != want.ChannelType {
		return true
	}
	baseURL, _ := account.Credentials["base_url"].(string)
	if !supplierManagedEndpointsEqual(baseURL, sourceEndpoint) {
		return true
	}
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		return true
	}
	if account.ProtocolEndpointCapability != nil &&
		identity.Key() != account.ProtocolEndpointCapability.CapabilityKey {
		return true
	}
	return false
}

func supplierAccountMatchesDesired(
	account *Account,
	source *SupplierSource,
	credential string,
	mapping map[string]string,
	priority int,
) bool {
	if source == nil {
		return false
	}
	return supplierAccountStructureMatches(account, source.Endpoint, source.ChannelType, credential, mapping) &&
		account.Priority == priority &&
		account.Concurrency == ResolveSupplierSourceAccountConcurrency(source.AccountConcurrency) &&
		supplierSchedulingProjectionMatches(account, mapping)
}

func supplierSchedulingProjectionMatches(account *Account, mapping map[string]string) bool {
	return account != nil && account.Status == StatusActive && account.Schedulable == (len(mapping) > 0)
}

func supplierProbeAccount(base *Account, source *SupplierSource, credential string, target supplierTargetBand) *Account {
	transport, err := resolveSupplierManagedTransport(source.Endpoint, source.ChannelType)
	if err != nil {
		transport = supplierManagedTransport{ChannelType: newapiconstant.ChannelTypeOpenAI, Endpoint: source.Endpoint}
	}
	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: transport.ChannelType,
		Concurrency: ResolveSupplierSourceAccountConcurrency(source.AccountConcurrency),
	}
	if base != nil {
		account = cloneSupplierProjectionAccount(base)
	}
	account.Name = supplierManagedAccountName(source, target.Band)
	account.Platform = PlatformNewAPI
	account.Type = AccountTypeAPIKey
	account.ChannelType = transport.ChannelType
	account.Credentials = supplierManagedCredentials(source.Endpoint, credential, target.Mapping, source.ChannelType)
	account.Extra = cloneSupplierJSONMap(account.Extra)
	if account.Extra == nil {
		account.Extra = make(map[string]any, 2)
	}
	account.Extra[SupplierSourceIDExtraKey] = source.ID
	account.Extra[SupplierDiscountBandExtraKey] = target.Band
	account.Priority = target.Priority
	account.Status = StatusActive
	account.Schedulable = false
	return account
}

func supplierAccountsByBand(accounts []*Account) (map[int]*Account, error) {
	result := make(map[int]*Account, len(accounts))
	for _, account := range accounts {
		band, ok := supplierDiscountBandFromAccount(account)
		if !ok {
			return nil, ErrSupplierSourceIdentityConflict
		}
		if _, duplicate := result[band]; duplicate {
			return nil, ErrSupplierSourceMultipleMatches
		}
		result[band] = cloneSupplierProjectionAccount(account)
	}
	return result, nil
}

func supplierProbeResultsFailed(results []SupplierProbeResult) bool {
	for _, result := range results {
		if result.Status != SupplierProbeStatusPassed {
			return true
		}
	}
	return false
}

func verifySupplierTargetPresent(account *Account, sourceID int64, band int, target supplierTargetBand) error {
	managedSourceID, sourceOK := supplierSourceIDFromAccount(account)
	managedBand, bandOK := supplierDiscountBandFromAccount(account)
	if !sourceOK || !bandOK || managedSourceID != sourceID || managedBand != band ||
		account.Priority != target.Priority || !account.Schedulable || account.Status != StatusActive {
		return ErrSupplierProjectionReadbackMismatch
	}
	actual := supplierModelMapping(account.Credentials)
	for clientModelID, upstreamModelID := range target.Mapping {
		if actual[clientModelID] != upstreamModelID {
			return ErrSupplierProjectionReadbackMismatch
		}
	}
	return nil
}

func supplierAdditionMapping(account *Account, target, allTargets map[string]string) map[string]string {
	result := make(map[string]string, len(target))
	if account != nil {
		for clientModelID, upstreamModelID := range supplierModelMapping(account.Credentials) {
			if targetUpstream, remainsTargeted := allTargets[clientModelID]; remainsTargeted && targetUpstream == upstreamModelID {
				result[clientModelID] = upstreamModelID
			}
		}
	}
	for clientModelID, upstreamModelID := range target {
		result[clientModelID] = upstreamModelID
	}
	return result
}

func supplierAllTargetMapping(targets map[int]supplierTargetBand) map[string]string {
	result := make(map[string]string)
	for _, target := range targets {
		for clientModelID, upstreamModelID := range target.Mapping {
			result[clientModelID] = upstreamModelID
		}
	}
	return result
}

func supplierAccountChange(before, after *Account, band int, action string) SupplierSourceAccountChange {
	change := SupplierSourceAccountChange{
		DiscountBand: band, Action: action, AddedModels: []string{}, RemovedModels: []string{},
	}
	if after != nil {
		change.AccountID = after.ID
		change.PriorityAfter = after.Priority
		change.SchedulableAfter = after.Schedulable
	}
	beforeMapping := map[string]string{}
	if before != nil {
		priorityBefore := before.Priority
		schedulableBefore := before.Schedulable
		change.PriorityBefore = &priorityBefore
		change.SchedulableBefore = &schedulableBefore
		beforeMapping = supplierModelMapping(before.Credentials)
	}
	afterMapping := map[string]string{}
	if after != nil {
		afterMapping = supplierModelMapping(after.Credentials)
	}
	for clientModelID, upstreamModelID := range afterMapping {
		if beforeMapping[clientModelID] != upstreamModelID {
			change.AddedModels = append(change.AddedModels, clientModelID)
		}
	}
	for clientModelID, upstreamModelID := range beforeMapping {
		if afterMapping[clientModelID] != upstreamModelID {
			change.RemovedModels = append(change.RemovedModels, clientModelID)
		}
	}
	sort.Strings(change.AddedModels)
	sort.Strings(change.RemovedModels)
	return change
}

func supplierSourceFromInput(input SupplierSourceInput, basePriority, accountConcurrency int) *SupplierSource {
	models := make([]SupplierSourceModel, 0, len(input.Models))
	for _, model := range input.Models {
		models = append(models, SupplierSourceModel(model))
	}
	return &SupplierSource{
		SupplierName: input.SupplierName, ChannelName: input.ChannelName, ChannelType: input.ChannelType,
		Endpoint:     input.Endpoint,
		BasePriority: basePriority, AccountConcurrency: ResolveSupplierSourceAccountConcurrency(accountConcurrency),
		Models: models, Notes: input.Notes,
	}
}

func (s *SupplierSourceService) ensureSupplierManagedConcurrency(
	ctx context.Context,
	source *SupplierSource,
	targets map[int]supplierTargetBand,
	workingByBand map[int]*Account,
	result *SupplierSourceSyncResult,
) error {
	wantConcurrency := ResolveSupplierSourceAccountConcurrency(source.AccountConcurrency)
	for _, band := range sortedSupplierBands(targets) {
		target := targets[band]
		if len(target.Mapping) == 0 {
			continue
		}
		account := workingByBand[band]
		if account == nil || account.Concurrency == wantConcurrency {
			continue
		}
		before := cloneSupplierProjectionAccount(account)
		updated, err := s.accounts.UpdateManagedAccountConcurrency(
			ctx, account.ID, source.ID, band, wantConcurrency,
		)
		if err != nil {
			result.FailedStep = fmt.Sprintf("concurrency_band_%d", band)
			return err
		}
		readback, readbackErr := s.accounts.GetAccount(ctx, updated.ID)
		if readbackErr != nil {
			result.FailedStep = fmt.Sprintf("concurrency_band_%d", band)
			return readbackErr
		}
		workingByBand[band] = readback
		result.Changes = append(result.Changes, supplierAccountChange(before, readback, band, "updated"))
	}
	return nil
}

func (s *SupplierSourceService) protectCredential(credential string) (string, string, error) {
	credential = strings.TrimSpace(credential)
	encrypted, err := s.encryptor.Encrypt(credential)
	if err != nil {
		return "", "", fmt.Errorf("encrypt supplier credential: %w", err)
	}
	fingerprint, err := s.fingerprinter.Fingerprint(credential)
	if err != nil {
		return "", "", fmt.Errorf("fingerprint supplier credential: %w", err)
	}
	return encrypted, fingerprint, nil
}

func supplierPriorityOverlapWarnings(entries []SupplierPriorityPreviewEntry) []SupplierPriorityPreviewWarning {
	sourceIDsByPriority := make(map[int]map[int64]struct{})
	for _, entry := range entries {
		if sourceIDsByPriority[entry.Priority] == nil {
			sourceIDsByPriority[entry.Priority] = make(map[int64]struct{})
		}
		sourceIDsByPriority[entry.Priority][entry.SourceID] = struct{}{}
	}
	priorities := make([]int, 0, len(sourceIDsByPriority))
	for priority, sourceIDs := range sourceIDsByPriority {
		if len(sourceIDs) > 1 {
			priorities = append(priorities, priority)
		}
	}
	sort.Ints(priorities)
	warnings := make([]SupplierPriorityPreviewWarning, 0, len(priorities))
	for _, priority := range priorities {
		sourceIDs := make([]int64, 0, len(sourceIDsByPriority[priority]))
		for sourceID := range sourceIDsByPriority[priority] {
			sourceIDs = append(sourceIDs, sourceID)
		}
		sort.Slice(sourceIDs, func(i, j int) bool { return sourceIDs[i] < sourceIDs[j] })
		warnings = append(warnings, SupplierPriorityPreviewWarning{
			Code: "priority_overlap", Priority: priority, SourceIDs: sourceIDs,
		})
	}
	return warnings
}

func supplierModelMapping(credentials map[string]any) map[string]string {
	result := make(map[string]string)
	if credentials == nil {
		return result
	}
	switch mapping := credentials["model_mapping"].(type) {
	case map[string]string:
		for clientModelID, upstreamModelID := range mapping {
			result[clientModelID] = upstreamModelID
		}
	case map[string]any:
		for clientModelID, rawUpstreamModelID := range mapping {
			if upstreamModelID, ok := rawUpstreamModelID.(string); ok {
				result[clientModelID] = upstreamModelID
			}
		}
	}
	return result
}

func supplierMappingIsSubsetOfTarget(existing, target map[string]string) bool {
	for clientModelID, upstreamModelID := range existing {
		if target[clientModelID] != upstreamModelID {
			return false
		}
	}
	return true
}

func supplierStringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	return supplierMappingIsSubsetOfTarget(left, right)
}

func cloneSupplierStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func sortedSupplierMappingKeys(mapping map[string]string) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSupplierBands(targets map[int]supplierTargetBand) []int {
	bands := make([]int, 0, len(targets))
	for band := range targets {
		bands = append(bands, band)
	}
	sort.Ints(bands)
	return bands
}

func sortedSupplierAccountBands(accounts map[int]*Account) []int {
	bands := make([]int, 0, len(accounts))
	for band := range accounts {
		bands = append(bands, band)
	}
	sort.Ints(bands)
	return bands
}

func supplierManagedAccountName(source *SupplierSource, band int) string {
	return fmt.Sprintf("%s/%s · 档位 %d", source.SupplierName, source.ChannelName, band)
}
