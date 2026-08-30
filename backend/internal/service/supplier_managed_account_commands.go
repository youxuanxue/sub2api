package service

import (
	"context"
	"maps"
	"strings"
)

type SupplierManagedAccountCreateInput struct {
	SourceID     int64
	DiscountBand int
	Name         string
	Endpoint     string
	Credential   string
	Priority     int
}

type SupplierManagedAccountUpdateInput struct {
	AccountID       int64
	SourceID        int64
	DiscountBand    int
	MetadataOnly    bool
	Adopt           bool
	Name            string
	Endpoint        string
	Credential      string
	ModelMapping    map[string]string
	Priority        int
	Status          string
	Schedulable     bool
	ChatProbePassed bool
}

type SupplierManagedAccountCommands interface {
	CreateSupplierManagedAccount(ctx context.Context, input SupplierManagedAccountCreateInput) (*Account, error)
	UpdateSupplierManagedAccount(ctx context.Context, input SupplierManagedAccountUpdateInput) (*Account, error)
}

type supplierProjectionAccountUpdater interface {
	UpdateSupplierMetadata(ctx context.Context, accountID int64, name string, priority int) error
	UpdateSupplierProjection(ctx context.Context, account *Account, chatProbePassed bool) error
}

type supplierManagedAccountReader interface {
	GetSupplierAccount(ctx context.Context, accountID int64) (*Account, error)
}

func ProvideSupplierManagedAccountCommands(admin AdminService) SupplierManagedAccountCommands {
	commands, ok := admin.(SupplierManagedAccountCommands)
	if !ok {
		panic("admin service does not implement supplier-managed account commands")
	}
	return commands
}

func (s *adminServiceImpl) CreateSupplierManagedAccount(
	ctx context.Context,
	input SupplierManagedAccountCreateInput,
) (*Account, error) {
	if err := validateSupplierManagedAccountCommand(
		input.SourceID,
		input.DiscountBand,
		input.Name,
		input.Endpoint,
		input.Credential,
	); err != nil {
		return nil, err
	}
	transport, err := resolveSupplierManagedTransport(input.Endpoint)
	if err != nil {
		return nil, err
	}
	initialSchedulable := false
	return s.createAccount(ctx, &CreateAccountInput{
		Name: input.Name, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: transport.ChannelType,
		Credentials: supplierManagedCredentials(input.Endpoint, input.Credential, map[string]string{}),
		Extra: map[string]any{
			SupplierSourceIDExtraKey:     input.SourceID,
			SupplierDiscountBandExtraKey: input.DiscountBand,
		},
		Concurrency:          1,
		Priority:             input.Priority,
		SkipDefaultGroupBind: true,
	}, accountCreateOptions{
		allowSupplierReservedExtra: true,
		initialSchedulable:         &initialSchedulable,
	})
}

func (s *adminServiceImpl) UpdateSupplierManagedAccount(
	ctx context.Context,
	input SupplierManagedAccountUpdateInput,
) (*Account, error) {
	if s == nil || s.accountRepo == nil || input.AccountID <= 0 {
		return nil, ErrSupplierSourceInvalidInput
	}
	reader, ok := s.accountRepo.(supplierManagedAccountReader)
	if !ok {
		return nil, ErrSupplierProjectionReaderMissing
	}
	account, err := reader.GetSupplierAccount(ctx, input.AccountID)
	if err != nil {
		return nil, err
	}
	account = cloneSupplierProjectionAccount(account)
	if err := validateSupplierManagedIdentity(input.SourceID, input.DiscountBand); err != nil {
		return nil, err
	}
	if input.Adopt {
		if IsSupplierManagedAccount(account) || input.MetadataOnly {
			return nil, ErrSupplierSourceIdentityConflict
		}
	} else {
		sourceID, sourceOK := supplierSourceIDFromAccount(account)
		band, bandOK := supplierDiscountBandFromAccount(account)
		if !sourceOK || !bandOK || sourceID != input.SourceID || band != input.DiscountBand {
			return nil, ErrSupplierSourceIdentityConflict
		}
	}
	if input.MetadataOnly {
		name := strings.TrimSpace(input.Name)
		if name == "" {
			return nil, ErrSupplierSourceInvalidInput
		}
		account.Name = name
		account.Priority = input.Priority
		if err := updateSupplierMetadata(ctx, s.accountRepo, account); err != nil {
			return nil, err
		}
		return account, nil
	}
	if err := validateSupplierManagedAccountCommand(
		input.SourceID,
		input.DiscountBand,
		input.Name,
		input.Endpoint,
		input.Credential,
	); err != nil {
		return nil, err
	}
	if len(input.ModelMapping) > 0 && !input.ChatProbePassed {
		return nil, ErrSupplierProjectionProtocolNotReady
	}

	transport, err := resolveSupplierManagedTransport(input.Endpoint)
	if err != nil {
		return nil, err
	}
	account.Name = strings.TrimSpace(input.Name)
	account.Platform = PlatformNewAPI
	account.Type = AccountTypeAPIKey
	account.ChannelType = transport.ChannelType
	account.Credentials = supplierManagedCredentials(input.Endpoint, input.Credential, input.ModelMapping)
	account.Extra = maps.Clone(account.Extra)
	if account.Extra == nil {
		account.Extra = make(map[string]any, 2)
	}
	account.Extra[SupplierSourceIDExtraKey] = input.SourceID
	account.Extra[SupplierDiscountBandExtraKey] = input.DiscountBand
	account.Priority = input.Priority
	if input.Status == "" {
		account.Status = StatusActive
	} else {
		account.Status = input.Status
	}
	account.Schedulable = input.Schedulable && len(input.ModelMapping) > 0
	if err := updateSupplierProjection(ctx, s.accountRepo, account, input.ChatProbePassed); err != nil {
		return nil, err
	}
	return account, nil
}

func updateSupplierMetadata(ctx context.Context, repo AccountRepository, account *Account) error {
	updater, ok := repo.(supplierProjectionAccountUpdater)
	if !ok {
		return ErrSupplierProjectionUpdaterMissing
	}
	return updater.UpdateSupplierMetadata(ctx, account.ID, account.Name, account.Priority)
}

func updateSupplierProjection(ctx context.Context, repo AccountRepository, account *Account, chatProbePassed bool) error {
	updater, ok := repo.(supplierProjectionAccountUpdater)
	if !ok {
		return ErrSupplierProjectionUpdaterMissing
	}
	return updater.UpdateSupplierProjection(ctx, account, chatProbePassed)
}

func validateSupplierManagedIdentity(sourceID int64, discountBand int) error {
	if sourceID <= 0 || discountBand < 1 || discountBand > 6 {
		return ErrSupplierSourceInvalidInput
	}
	return nil
}

func validateSupplierManagedAccountCommand(sourceID int64, discountBand int, name, endpoint, credential string) error {
	if err := validateSupplierManagedIdentity(sourceID, discountBand); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(credential) == "" {
		return ErrSupplierSourceInvalidInput
	}
	if _, err := NormalizeSupplierEndpoint(endpoint); err != nil {
		return err
	}
	return nil
}

func supplierManagedCredentials(endpoint, credential string, modelMapping map[string]string) map[string]any {
	mapping := make(map[string]any, len(modelMapping))
	for clientModelID, upstreamModelID := range modelMapping {
		mapping[clientModelID] = upstreamModelID
	}
	baseURL := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if transport, err := resolveSupplierManagedTransport(endpoint); err == nil {
		baseURL = transport.Endpoint
	}
	return map[string]any{
		"base_url":      baseURL,
		"api_key":       strings.TrimSpace(credential),
		"model_mapping": mapping,
	}
}
