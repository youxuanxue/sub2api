package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUS048_SupplierAccountStoreMatchesWithoutReadingGroups(t *testing.T) {
	repo := &supplierAccountStoreRepoFake{accounts: []Account{{
		ID: 90, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 46,
		Credentials: map[string]any{
			"base_url": "https://qianfan.baidubce.com/v1/", "api_key": "secret",
			"model_mapping": map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"},
		},
		AccountGroups: []AccountGroup{{GroupID: 4}},
		GroupIDs:      []int64{4, 9},
		Groups:        []*Group{{ID: 4}},
		Extra: map[string]any{
			SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3,
		},
	}}}
	commands := &supplierAccountCommandsFake{}
	store := NewSupplierSourceAccountStore(repo, commands, supplierSourceTestFingerprinter{})

	matches, err := store.FindCredentialEndpointMatches(context.Background(), SupplierAccountMatch{
		Endpoint: "https://qianfan.baidubce.com/v1", CredentialFingerprint: "fp:secret",
	})

	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.Equal(t, int64(90), matches[0].ID)
	require.Empty(t, matches[0].AccountGroups)
	require.Empty(t, matches[0].GroupIDs)
	require.Empty(t, matches[0].Groups)

	managed, err := store.ListManagedAccounts(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, managed, 1)
	require.Empty(t, managed[0].AccountGroups)
	require.Empty(t, managed[0].GroupIDs)
	require.Empty(t, managed[0].Groups)

	account, err := store.GetAccount(context.Background(), 90)
	require.NoError(t, err)
	require.Empty(t, account.AccountGroups)
	require.Empty(t, account.GroupIDs)
	require.Empty(t, account.Groups)
	require.Zero(t, repo.genericReadCalls, "supplier-source reads must not call relation-loading account repository methods")
}

func TestUS048_SupplierAccountStoreDelegatesWritesOnlyToAccountCommands(t *testing.T) {
	repo := &supplierAccountStoreRepoFake{}
	commands := &supplierAccountCommandsFake{}
	store := NewSupplierSourceAccountStore(repo, commands, supplierSourceTestFingerprinter{})

	created, err := store.CreateManagedAccount(context.Background(), SupplierManagedAccountCreateInput{
		SourceID: 7, DiscountBand: 3, Name: "source-7-band-3", Endpoint: "https://supplier.example/v1",
		Credential: "secret", Priority: 103,
	})
	require.NoError(t, err)
	require.Equal(t, int64(101), created.ID)
	require.Equal(t, 1, commands.createCalls)

	updated, err := store.UpdateManagedAccount(context.Background(), SupplierManagedAccountUpdateInput{
		AccountID: 101, SourceID: 7, DiscountBand: 3, Name: "source-7-band-3",
		Endpoint: "https://supplier.example/v1", Credential: "secret",
		ModelMapping: map[string]string{"model": "upstream"}, Priority: 103, Status: StatusActive, Schedulable: true,
	})
	require.NoError(t, err)
	require.Equal(t, int64(101), updated.ID)
	require.Equal(t, 1, commands.updateCalls)
	require.Zero(t, repo.createCalls)
	require.Zero(t, repo.updateCalls)
}

type supplierAccountStoreRepoFake struct {
	AccountRepository
	accounts         []Account
	createCalls      int
	updateCalls      int
	genericReadCalls int
}

func (r *supplierAccountStoreRepoFake) ListByPlatform(context.Context, string) ([]Account, error) {
	r.genericReadCalls++
	return nil, errors.New("generic relation-loading list must not be used")
}

func (r *supplierAccountStoreRepoFake) FindByExtraField(context.Context, string, any) ([]Account, error) {
	r.genericReadCalls++
	return nil, errors.New("generic relation-loading extra lookup must not be used")
}

func (r *supplierAccountStoreRepoFake) ListSupplierManagedAccounts(context.Context, int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *supplierAccountStoreRepoFake) ListSupplierAdoptionCandidates(context.Context) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *supplierAccountStoreRepoFake) GetByID(_ context.Context, id int64) (*Account, error) {
	r.genericReadCalls++
	return nil, errors.New("generic relation-loading account lookup must not be used")
}

func (r *supplierAccountStoreRepoFake) GetSupplierAccount(_ context.Context, id int64) (*Account, error) {
	for index := range r.accounts {
		if r.accounts[index].ID == id {
			return cloneSupplierProjectionAccount(&r.accounts[index]), nil
		}
	}
	return nil, ErrAccountNotFound
}

func (r *supplierAccountStoreRepoFake) Create(context.Context, *Account) error {
	r.createCalls++
	return nil
}

func (r *supplierAccountStoreRepoFake) Update(context.Context, *Account) error {
	r.updateCalls++
	return nil
}

type supplierAccountCommandsFake struct {
	createCalls int
	updateCalls int
}

func (f *supplierAccountCommandsFake) CreateSupplierManagedAccount(_ context.Context, input SupplierManagedAccountCreateInput) (*Account, error) {
	f.createCalls++
	return &Account{ID: 101, Extra: map[string]any{
		SupplierSourceIDExtraKey: input.SourceID, SupplierDiscountBandExtraKey: input.DiscountBand,
	}}, nil
}

func (f *supplierAccountCommandsFake) UpdateSupplierManagedAccount(_ context.Context, input SupplierManagedAccountUpdateInput) (*Account, error) {
	f.updateCalls++
	return &Account{ID: input.AccountID}, nil
}
