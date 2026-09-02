package service

import (
	"context"
	"errors"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestUS048_SupplierCreateStartsEmptyUngroupedAndUnschedulable(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{}
	groups := &supplierManagedCommandsGroupRepoFake{groups: []Group{{ID: 9, Name: PlatformNewAPI + "-default", Platform: PlatformNewAPI}}}
	svc := &adminServiceImpl{accountRepo: accounts, groupRepo: groups}

	created, err := svc.CreateSupplierManagedAccount(context.Background(), SupplierManagedAccountCreateInput{
		SourceID: 7, DiscountBand: 3, Name: "佳杰/stbl-5 · 档位 3",
		Endpoint: "https://token.vstecscloud.com/v1", Credential: "secret",
		Priority: 130,
	})

	require.NoError(t, err)
	require.NotNil(t, accounts.created)
	require.Empty(t, accounts.created.GetModelMapping(), "new supplier account must be an empty projection until a verified update commits")
	require.False(t, accounts.created.Schedulable, "account must never be persisted as schedulable before projection completes")
	require.Equal(t, StatusActive, accounts.created.Status)
	require.Empty(t, created.GroupIDs)
	require.Zero(t, accounts.bindCalls, "supplier create must not bind account groups")
	require.Zero(t, groups.listCalls, "supplier create must not read account groups")
	require.Equal(t, int64(7), accounts.created.Extra[SupplierSourceIDExtraKey])
	require.Equal(t, 3, accounts.created.Extra[SupplierDiscountBandExtraKey])
}

func TestUS048_OrdinaryCreateAccountStillFailsWhenDefaultGroupBindFails(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{bindErr: errors.New("injected group bind failure")}
	groups := &supplierManagedCommandsGroupRepoFake{groups: []Group{{ID: 9, Name: PlatformNewAPI + "-default", Platform: PlatformNewAPI}}}
	svc := &adminServiceImpl{accountRepo: accounts, groupRepo: groups}

	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "ordinary", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": "https://ordinary.example/v1", "api_key": "secret"},
	})

	require.ErrorContains(t, err, "injected group bind failure")
	require.Nil(t, created, "skipping groups on the supplier path must not change the public CreateAccount failure contract")
	require.Equal(t, 1, accounts.bindCalls)
}

func TestUS048_SupplierCreateUsesDefaultAccountConcurrency(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{}
	groups := &supplierManagedCommandsGroupRepoFake{groups: []Group{{ID: 9, Name: PlatformNewAPI + "-default", Platform: PlatformNewAPI}}}
	svc := &adminServiceImpl{accountRepo: accounts, groupRepo: groups}

	_, err := svc.CreateSupplierManagedAccount(context.Background(), SupplierManagedAccountCreateInput{
		SourceID: 7, DiscountBand: 3, Name: "supplier/test · 档位 3",
		Endpoint: "https://supplier.example/v1", Credential: "secret", Priority: 130,
	})

	require.NoError(t, err)
	require.Equal(t, SupplierSourceDefaultAccountConcurrency, accounts.created.Concurrency)
}

func TestUS048_SupplierConcurrencyUpdateUsesNarrowWriteWithoutProbeEvidence(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{existing: &Account{
		ID: 41,
		Extra: map[string]any{
			SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3,
		},
		Concurrency: 1,
	}}
	svc := &adminServiceImpl{accountRepo: accounts}

	updated, err := svc.UpdateSupplierManagedAccountConcurrency(context.Background(), 41, 7, 3, 1000)

	require.NoError(t, err)
	require.Equal(t, 1000, updated.Concurrency)
	require.Equal(t, 1, accounts.concurrencyUpdateCalls)
	require.Equal(t, int64(41), accounts.concurrencyAccountID)
	require.Equal(t, 1000, accounts.concurrencyValue)
	require.Zero(t, accounts.projectionUpdateCalls, "concurrency-only changes must not publish protocol evidence")
	require.Zero(t, accounts.genericUpdateCalls)
}

func TestUS048_SupplierConfigurationUpdateUsesGroupFreeReadAndNarrowWrite(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{existing: &Account{
		ID: 41, Name: "old", Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 46,
		Credentials: map[string]any{"base_url": "https://old.example/v1", "api_key": "old", "model_mapping": map[string]string{"old": "old"}},
		Extra: map[string]any{
			SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3, "runtime_observation": "keep",
		},
		Priority: 130, Status: StatusActive, Schedulable: true, GroupIDs: []int64{4, 9},
	}}
	svc := &adminServiceImpl{accountRepo: accounts}

	updated, err := svc.UpdateSupplierManagedAccount(context.Background(), SupplierManagedAccountUpdateInput{
		AccountID: 41, SourceID: 7, DiscountBand: 3, Name: "佳杰/stbl-5 · 档位 3",
		Endpoint: "https://new.example/v1", Credential: "new-secret",
		ModelMapping: map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"},
		Priority:     123, Status: StatusActive, Schedulable: true, ProtocolProbePassed: true,
	})

	require.NoError(t, err)
	require.Empty(t, updated.GroupIDs, "supplier projection commands must not load account-group data")
	require.Empty(t, accounts.updated.GroupIDs)
	require.Zero(t, accounts.bindCalls, "supplier update must not call account-group APIs")
	require.Zero(t, accounts.genericGetCalls, "supplier update must not use relation-loading account reads")
	require.Equal(t, 1, accounts.supplierGetCalls)
	require.Equal(t, "keep", accounts.updated.Extra["runtime_observation"])
	require.Equal(t, "https://new.example/v1", accounts.updated.Credentials["base_url"])
	require.Equal(t, 123, accounts.updated.Priority)
	require.Equal(t, PlatformNewAPI, accounts.updated.Platform)
	require.Equal(t, AccountTypeAPIKey, accounts.updated.Type)
	require.Equal(t, newapiconstant.ChannelTypeOpenAI, accounts.updated.ChannelType, "generic supplier hosts keep the OpenAI Chat transport")
	require.Equal(t, 1, accounts.projectionUpdateCalls)
	require.True(t, accounts.projectionProtocolProbePassed)
	require.Zero(t, accounts.genericUpdateCalls, "supplier projections must not use the generic account update path")
}

func TestUS048_SupplierQianfanCreateUsesBaiduV2Transport(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{}
	groups := &supplierManagedCommandsGroupRepoFake{groups: []Group{{ID: 9, Name: PlatformNewAPI + "-default", Platform: PlatformNewAPI}}}
	svc := &adminServiceImpl{accountRepo: accounts, groupRepo: groups}

	_, err := svc.CreateSupplierManagedAccount(context.Background(), SupplierManagedAccountCreateInput{
		SourceID: 7, DiscountBand: 3, Name: "百度/千帆 · 档位 3",
		Endpoint: "https://qianfan.baidubce.com/v2", Credential: "secret",
		Priority: 130,
	})

	require.NoError(t, err)
	require.Equal(t, newapiconstant.ChannelTypeBaiduV2, accounts.created.ChannelType)
	require.Equal(t, newapiintegration.QianfanBaseURL, accounts.created.Credentials["base_url"])
	require.False(t, accounts.created.Schedulable)
	require.Zero(t, accounts.bindCalls)
}

func TestUS048_SupplierAnthropicCreateDeclaresMessagesExclusive(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{}
	groups := &supplierManagedCommandsGroupRepoFake{groups: []Group{{ID: 9, Name: PlatformNewAPI + "-default", Platform: PlatformNewAPI}}}
	svc := &adminServiceImpl{accountRepo: accounts, groupRepo: groups}

	_, err := svc.CreateSupplierManagedAccount(context.Background(), SupplierManagedAccountCreateInput{
		SourceID: 9, DiscountBand: 6, Name: "CloudWise/Anthropic · 档位 6",
		Endpoint: "https://api.cloudwise.ai/api", Credential: "secret",
		ChannelType: newapiconstant.ChannelTypeAnthropic, Priority: 160,
	})

	require.NoError(t, err)
	require.Equal(t, newapiconstant.ChannelTypeAnthropic, accounts.created.ChannelType)
	require.Equal(t, "https://api.cloudwise.ai/api", accounts.created.Credentials["base_url"])
	require.Equal(t, true, accounts.created.Credentials[ProtocolEndpointsExclusiveCredentialKey])
	require.Equal(t, map[string]any{
		APIProtocolAnthropic: "https://api.cloudwise.ai/api",
	}, accounts.created.Credentials[apiBaseURLsCredentialKey])
	require.False(t, accounts.created.Schedulable)
	require.Zero(t, accounts.bindCalls)
}

func TestUS048_SupplierConfigurationUpdateRequiresPassedProtocolProbe(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{existing: &Account{
		ID: 41, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: supplierManagedCredentials(
			"https://old.example/v1", "old-secret", map[string]string{"old-model": "old-model"}, 1),
		Extra: map[string]any{
			SupplierSourceIDExtraKey:     int64(7),
			SupplierDiscountBandExtraKey: 3,
		},
		Priority: 130, Status: StatusActive, Schedulable: true,
	}}
	svc := &adminServiceImpl{accountRepo: accounts}

	_, err := svc.UpdateSupplierManagedAccount(context.Background(), SupplierManagedAccountUpdateInput{
		AccountID: 41, SourceID: 7, DiscountBand: 3, Name: "佳杰/stbl-5 · 档位 3",
		Endpoint: "https://new.example/v1", Credential: "new-secret",
		ModelMapping: map[string]string{"new-model": "new-model"},
		Priority:     103, Status: StatusActive, Schedulable: true,
	})

	require.ErrorIs(t, err, ErrSupplierProjectionProtocolNotReady)
	require.Zero(t, accounts.projectionUpdateCalls)
}

func TestUS048_SupplierMetadataUpdateChangesOnlyNameAndPriority(t *testing.T) {
	existing := &Account{
		ID: 41, Name: "佳杰/stbl-5 · 档位 3", Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 46,
		Credentials: map[string]any{
			"base_url": "https://supplier.example/v1", "api_key": "secret",
			"model_mapping": map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"},
		},
		Extra: map[string]any{
			SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3, "runtime_observation": "keep",
		},
		Priority: 130, Status: StatusActive, Schedulable: true, GroupIDs: []int64{4, 9},
	}
	accounts := &supplierManagedCommandsAccountRepoFake{existing: existing}
	svc := &adminServiceImpl{accountRepo: accounts}

	updated, err := svc.UpdateSupplierManagedAccount(context.Background(), SupplierManagedAccountUpdateInput{
		AccountID: 41, SourceID: 7, DiscountBand: 3,
		Name: "佳杰科技/stbl-6 · 档位 3", Priority: 111, MetadataOnly: true,
	})

	require.NoError(t, err)
	require.Equal(t, 111, updated.Priority)
	require.Equal(t, "佳杰科技/stbl-6 · 档位 3", updated.Name)
	require.Equal(t, existing.ChannelType, updated.ChannelType)
	require.Equal(t, existing.Credentials, updated.Credentials)
	require.Equal(t, existing.Extra, updated.Extra)
	require.Equal(t, existing.Status, updated.Status)
	require.Equal(t, existing.Schedulable, updated.Schedulable)
	require.Empty(t, updated.GroupIDs)
	require.Zero(t, accounts.bindCalls)
	require.Zero(t, accounts.genericGetCalls)
	require.Equal(t, 1, accounts.supplierGetCalls)
	require.Equal(t, 1, accounts.metadataUpdateCalls)
	require.Zero(t, accounts.projectionUpdateCalls, "metadata-only changes must not enter the structural projection writer")
	require.Zero(t, accounts.genericUpdateCalls)
	require.Equal(t, int64(41), accounts.metadataAccountID)
	require.Equal(t, "佳杰科技/stbl-6 · 档位 3", accounts.metadataName)
	require.Equal(t, 111, accounts.metadataPriority)
}

func TestUS048_SupplierAdoptAddsManagedIdentityWithoutReadingOrWritingGroups(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{existing: &Account{
		ID: 90, Name: "百度千帆", Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 46,
		Credentials: map[string]any{
			"base_url": "https://qianfan.baidubce.com/v1", "api_key": "secret",
			"model_mapping": map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"},
		},
		Extra:    map[string]any{"runtime_observation": "keep"},
		Priority: 200, Status: StatusActive, Schedulable: true, GroupIDs: []int64{4, 9},
	}}
	svc := &adminServiceImpl{accountRepo: accounts}

	updated, err := svc.UpdateSupplierManagedAccount(context.Background(), SupplierManagedAccountUpdateInput{
		AccountID: 90, SourceID: 7, DiscountBand: 3, Name: "百度/千帆 · 档位 3",
		Endpoint: "https://qianfan.baidubce.com/v1", Credential: "secret",
		ModelMapping: map[string]string{"deepseek-v4-pro": "deepseek-v4-pro", "qwen": "qwen"},
		Priority:     103, Status: StatusActive, Schedulable: true, Adopt: true, ProtocolProbePassed: true,
	})

	require.NoError(t, err)
	require.Equal(t, int64(7), accounts.updated.Extra[SupplierSourceIDExtraKey])
	require.Equal(t, 3, accounts.updated.Extra[SupplierDiscountBandExtraKey])
	require.Equal(t, "keep", accounts.updated.Extra["runtime_observation"])
	require.Equal(t, newapiconstant.ChannelTypeBaiduV2, accounts.updated.ChannelType)
	require.Equal(t, newapiintegration.QianfanBaseURL, accounts.updated.Credentials["base_url"])
	require.Empty(t, accounts.updated.GroupIDs)
	require.Empty(t, updated.GroupIDs)
	require.Zero(t, accounts.bindCalls)
	require.Zero(t, accounts.genericGetCalls)
	require.Equal(t, 1, accounts.supplierGetCalls)
}

func TestUS048_SupplierUpdateRejectsCrossSourceOrCrossBandTakeover(t *testing.T) {
	accounts := &supplierManagedCommandsAccountRepoFake{existing: &Account{
		ID: 41, Extra: map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
	}}
	svc := &adminServiceImpl{accountRepo: accounts}

	_, err := svc.UpdateSupplierManagedAccount(context.Background(), SupplierManagedAccountUpdateInput{
		AccountID: 41, SourceID: 8, DiscountBand: 3,
	})
	require.ErrorIs(t, err, ErrSupplierSourceIdentityConflict)

	_, err = svc.UpdateSupplierManagedAccount(context.Background(), SupplierManagedAccountUpdateInput{
		AccountID: 41, SourceID: 7, DiscountBand: 4,
	})
	require.ErrorIs(t, err, ErrSupplierSourceIdentityConflict)
}

type supplierManagedCommandsAccountRepoFake struct {
	AccountRepository
	existing                      *Account
	created                       *Account
	updated                       *Account
	boundGroupIDs                 []int64
	bindCalls                     int
	genericUpdateCalls            int
	projectionUpdateCalls         int
	projectionProtocolProbePassed bool
	metadataUpdateCalls           int
	metadataAccountID             int64
	metadataName                  string
	metadataPriority              int
	concurrencyUpdateCalls        int
	concurrencyAccountID          int64
	concurrencyValue              int
	bindErr                       error
	genericGetCalls               int
	supplierGetCalls              int
}

func (r *supplierManagedCommandsAccountRepoFake) Create(_ context.Context, account *Account) error {
	copyAccount := cloneSupplierManagedCommandsTestAccount(account)
	copyAccount.ID = 101
	r.created = copyAccount
	account.ID = 101
	return nil
}

func (r *supplierManagedCommandsAccountRepoFake) GetByID(context.Context, int64) (*Account, error) {
	r.genericGetCalls++
	if r.existing == nil {
		return nil, ErrAccountNotFound
	}
	return cloneSupplierManagedCommandsTestAccount(r.existing), nil
}

func (r *supplierManagedCommandsAccountRepoFake) GetSupplierAccount(context.Context, int64) (*Account, error) {
	r.supplierGetCalls++
	if r.existing == nil {
		return nil, ErrAccountNotFound
	}
	return cloneSupplierProjectionAccount(r.existing), nil
}

func (r *supplierManagedCommandsAccountRepoFake) Update(_ context.Context, account *Account) error {
	r.genericUpdateCalls++
	r.updated = cloneSupplierManagedCommandsTestAccount(account)
	return nil
}

func (r *supplierManagedCommandsAccountRepoFake) UpdateSupplierProjection(_ context.Context, account *Account, protocolProbePassed bool) error {
	r.projectionUpdateCalls++
	r.projectionProtocolProbePassed = protocolProbePassed
	r.updated = cloneSupplierManagedCommandsTestAccount(account)
	return nil
}

func (r *supplierManagedCommandsAccountRepoFake) UpdateSupplierMetadata(_ context.Context, accountID int64, name string, priority int) error {
	r.metadataUpdateCalls++
	r.metadataAccountID = accountID
	r.metadataName = name
	r.metadataPriority = priority
	r.updated = cloneSupplierManagedCommandsTestAccount(r.existing)
	r.updated.Name = name
	r.updated.Priority = priority
	return nil
}

func (r *supplierManagedCommandsAccountRepoFake) UpdateSupplierConcurrency(_ context.Context, accountID int64, concurrency int) error {
	r.concurrencyUpdateCalls++
	r.concurrencyAccountID = accountID
	r.concurrencyValue = concurrency
	r.updated = cloneSupplierManagedCommandsTestAccount(r.existing)
	r.updated.Concurrency = concurrency
	return nil
}

func cloneSupplierManagedCommandsTestAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	copyAccount := *account
	copyAccount.Credentials = cloneSupplierJSONMap(account.Credentials)
	copyAccount.Extra = cloneSupplierJSONMap(account.Extra)
	copyAccount.AccountGroups = append([]AccountGroup(nil), account.AccountGroups...)
	copyAccount.GroupIDs = append([]int64(nil), account.GroupIDs...)
	copyAccount.Groups = append([]*Group(nil), account.Groups...)
	return &copyAccount
}

func (r *supplierManagedCommandsAccountRepoFake) BindGroups(_ context.Context, _ int64, groupIDs []int64) error {
	r.bindCalls++
	r.boundGroupIDs = append([]int64(nil), groupIDs...)
	return r.bindErr
}

func (r *supplierManagedCommandsAccountRepoFake) ListByGroup(context.Context, int64) ([]Account, error) {
	return nil, nil
}

type supplierManagedCommandsGroupRepoFake struct {
	GroupRepository
	groups    []Group
	listCalls int
}

func (r *supplierManagedCommandsGroupRepoFake) ListActiveByPlatform(context.Context, string) ([]Group, error) {
	r.listCalls++
	return append([]Group(nil), r.groups...), nil
}

func (r *supplierManagedCommandsGroupRepoFake) GetByID(_ context.Context, id int64) (*Group, error) {
	for index := range r.groups {
		if r.groups[index].ID == id {
			group := r.groups[index]
			return &group, nil
		}
	}
	return nil, ErrGroupNotFound
}
