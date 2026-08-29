package service

import (
	"context"
	"strings"
)

type defaultSupplierSourceAccountStore struct {
	reader        supplierSourceAccountReader
	commands      SupplierManagedAccountCommands
	fingerprinter SupplierCredentialFingerprinter
}

type supplierSourceAccountReader interface {
	ListSupplierManagedAccounts(ctx context.Context, sourceID int64) ([]Account, error)
	ListSupplierAdoptionCandidates(ctx context.Context) ([]Account, error)
	GetSupplierAccount(ctx context.Context, accountID int64) (*Account, error)
}

func NewSupplierSourceAccountStore(
	accountRepo AccountRepository,
	commands SupplierManagedAccountCommands,
	fingerprinter SupplierCredentialFingerprinter,
) SupplierSourceAccountStore {
	reader, _ := accountRepo.(supplierSourceAccountReader)
	return &defaultSupplierSourceAccountStore{
		reader: reader, commands: commands, fingerprinter: fingerprinter,
	}
}

func (s *defaultSupplierSourceAccountStore) ListManagedAccounts(ctx context.Context, sourceID int64) ([]*Account, error) {
	if s == nil || s.reader == nil {
		return nil, ErrSupplierProjectionReaderMissing
	}
	accounts, err := s.reader.ListSupplierManagedAccounts(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	result := make([]*Account, 0, len(accounts))
	for index := range accounts {
		account := &accounts[index]
		managedSourceID, ok := supplierSourceIDFromAccount(account)
		if !ok || managedSourceID != sourceID {
			continue
		}
		result = append(result, cloneSupplierProjectionAccount(account))
	}
	return result, nil
}

func (s *defaultSupplierSourceAccountStore) FindCredentialEndpointMatches(
	ctx context.Context,
	match SupplierAccountMatch,
) ([]*Account, error) {
	if s == nil || s.reader == nil {
		return nil, ErrSupplierProjectionReaderMissing
	}
	accounts, err := s.reader.ListSupplierAdoptionCandidates(ctx)
	if err != nil {
		return nil, err
	}
	wantEndpoint, err := NormalizeSupplierEndpoint(match.Endpoint)
	if err != nil {
		return nil, err
	}
	result := make([]*Account, 0)
	for index := range accounts {
		account := &accounts[index]
		baseURL, _ := account.Credentials["base_url"].(string)
		endpoint, endpointErr := NormalizeSupplierEndpoint(baseURL)
		if endpointErr != nil || endpoint != wantEndpoint {
			continue
		}
		apiKey, _ := account.Credentials["api_key"].(string)
		fingerprint, fingerprintErr := s.fingerprinter.Fingerprint(strings.TrimSpace(apiKey))
		if fingerprintErr != nil || !hmacEqualString(fingerprint, match.CredentialFingerprint) {
			continue
		}
		result = append(result, cloneSupplierProjectionAccount(account))
	}
	return result, nil
}

func (s *defaultSupplierSourceAccountStore) CreateManagedAccount(
	ctx context.Context,
	input SupplierManagedAccountCreateInput,
) (*Account, error) {
	account, err := s.commands.CreateSupplierManagedAccount(ctx, input)
	return cloneSupplierProjectionAccount(account), err
}

func (s *defaultSupplierSourceAccountStore) UpdateManagedAccount(
	ctx context.Context,
	input SupplierManagedAccountUpdateInput,
) (*Account, error) {
	account, err := s.commands.UpdateSupplierManagedAccount(ctx, input)
	return cloneSupplierProjectionAccount(account), err
}

func (s *defaultSupplierSourceAccountStore) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.reader == nil {
		return nil, ErrSupplierProjectionReaderMissing
	}
	account, err := s.reader.GetSupplierAccount(ctx, accountID)
	return cloneSupplierProjectionAccount(account), err
}

func hmacEqualString(left, right string) bool {
	if len(left) != len(right) || left == "" {
		return false
	}
	var diff byte
	for index := range left {
		diff |= left[index] ^ right[index]
	}
	return diff == 0
}

func cloneSupplierProjectionAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	copyAccount := *account
	copyAccount.Credentials = cloneSupplierJSONMap(account.Credentials)
	copyAccount.Extra = cloneSupplierJSONMap(account.Extra)
	copyAccount.AccountGroups = nil
	copyAccount.GroupIDs = nil
	copyAccount.Groups = nil
	return &copyAccount
}

func cloneSupplierJSONMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case map[string]string:
			copyMap := make(map[string]string, len(typed))
			for mapKey, mapValue := range typed {
				copyMap[mapKey] = mapValue
			}
			result[key] = copyMap
		case map[string]any:
			result[key] = cloneSupplierJSONMap(typed)
		default:
			result[key] = value
		}
	}
	return result
}
