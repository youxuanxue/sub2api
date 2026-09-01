package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUS048_ManagedAccountUsesSupplierSourceIDAsOnlyMarker(t *testing.T) {
	require.True(t, IsSupplierManagedAccount(&Account{Extra: map[string]any{SupplierSourceIDExtraKey: int64(7)}}))
	require.True(t, IsSupplierManagedAccount(&Account{Extra: map[string]any{SupplierSourceIDExtraKey: "malformed"}}), "presence must fail closed")
	require.False(t, IsSupplierManagedAccount(&Account{Extra: map[string]any{"tk_source_class": "supplier"}}))
	require.False(t, IsSupplierManagedAccount(&Account{}))
}

func TestHasSupplierManagedTransportIdentity_SchedulerSafeSSOT(t *testing.T) {
	exclusiveOnly := &Account{
		Credentials: map[string]any{ProtocolEndpointsExclusiveCredentialKey: true},
	}
	extraOnly := &Account{
		Extra: map[string]any{SupplierSourceIDExtraKey: int64(7)},
	}
	neither := &Account{}
	both := &Account{
		Credentials: map[string]any{ProtocolEndpointsExclusiveCredentialKey: true},
		Extra:       map[string]any{SupplierSourceIDExtraKey: int64(7)},
	}

	require.True(t, HasSupplierManagedTransportIdentity(exclusiveOnly),
		"exclusive credential alone must light the hot-path transport identity (scheduler Extra omits supplier_source_id)")
	require.False(t, IsSupplierManagedAccount(exclusiveOnly))
	require.True(t, HasSupplierManagedTransportIdentity(extraOnly),
		"full DB loads with Extra.supplier_source_id remain recognized")
	require.False(t, accountDeclaresExclusiveProtocolEndpoints(extraOnly))
	require.False(t, HasSupplierManagedTransportIdentity(neither))
	require.True(t, HasSupplierManagedTransportIdentity(both))
	require.False(t, HasSupplierManagedTransportIdentity(nil))
}

func TestUS048_OrdinaryCreateRejectsSupplierReservedExtra(t *testing.T) {
	for _, key := range []string{SupplierSourceIDExtraKey, SupplierDiscountBandExtraKey} {
		err := ValidateSupplierReservedAccountExtra(map[string]any{key: 7})
		require.ErrorIs(t, err, ErrSupplierReservedAccountExtra)
	}
	require.NoError(t, ValidateSupplierReservedAccountExtra(map[string]any{"operator_note": "ok"}))
}

func TestUS048_ManagedAccountBehavesLikeOrdinaryAccountForUpdates(t *testing.T) {
	repo := newManagedAccountAdminRepoFake()
	groups := &managedAccountGroupRepoFake{ids: map[int64]bool{11: true}}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: groups}
	priority := 50
	groupIDs := []int64{11}
	notes := "ops"
	concurrency := 3

	updated, err := svc.UpdateAccount(context.Background(), repo.account.ID, &UpdateAccountInput{
		Name: "renamed-managed", Status: StatusDisabled, Concurrency: &concurrency,
		Priority: &priority, GroupIDs: &groupIDs, Notes: &notes,
		Credentials:           map[string]any{"api_key": "rotated"},
		SkipMixedChannelCheck: true,
	})
	require.NoError(t, err)
	require.Equal(t, "renamed-managed", updated.Name)
	require.Equal(t, StatusDisabled, updated.Status)
	require.Equal(t, 50, updated.Priority)
	require.Equal(t, []int64{11}, repo.boundGroups[repo.account.ID])
	_, hasSource := repo.updated.Extra[SupplierSourceIDExtraKey]
	require.True(t, hasSource, "ordinary Extra edits must preserve supplier identity")

	_, err = svc.SetAccountSchedulable(context.Background(), repo.account.ID, false)
	require.NoError(t, err)
	require.Equal(t, 1, repo.setSchedulableCalls)

	err = svc.DeleteAccount(context.Background(), repo.account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, repo.deleteCalls)
}

func TestUS048_ManagedAccountDuplicateStripsSupplierIdentity(t *testing.T) {
	extra, err := duplicateAccountExtra(map[string]any{
		SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3, "operator_note": "keep",
	})
	require.NoError(t, err)
	require.Equal(t, "keep", extra["operator_note"])
	_, hasSource := extra[SupplierSourceIDExtraKey]
	_, hasBand := extra[SupplierDiscountBandExtraKey]
	require.False(t, hasSource)
	require.False(t, hasBand)
}

func TestUS048_UnmanagedAccountCannotForgeSupplierManagedExtra(t *testing.T) {
	tests := []struct {
		name string
		run  func(*adminServiceImpl, *managedAccountAdminRepoFake) error
	}{
		{
			name: "generic account update",
			run: func(svc *adminServiceImpl, repo *managedAccountAdminRepoFake) error {
				_, err := svc.UpdateAccount(context.Background(), repo.account.ID, &UpdateAccountInput{
					Extra: map[string]any{SupplierSourceIDExtraKey: int64(7)},
				})
				return err
			},
		},
		{
			name: "direct extra update",
			run: func(svc *adminServiceImpl, repo *managedAccountAdminRepoFake) error {
				return svc.UpdateAccountExtra(
					context.Background(), repo.account.ID,
					map[string]any{SupplierDiscountBandExtraKey: 3},
				)
			},
		},
		{
			name: "bulk update",
			run: func(svc *adminServiceImpl, repo *managedAccountAdminRepoFake) error {
				_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
					AccountIDs: []int64{repo.account.ID},
					Extra:      map[string]any{SupplierSourceIDExtraKey: int64(7)},
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newManagedAccountAdminRepoFake()
			repo.account.Extra = map[string]any{"operator_note": "ordinary account"}
			svc := &adminServiceImpl{accountRepo: repo}

			err := tt.run(svc, repo)

			require.ErrorIs(t, err, ErrSupplierReservedAccountExtra)
			require.Zero(t, repo.updateCalls)
			require.Zero(t, repo.updateExtraCalls)
			require.Zero(t, repo.bulkUpdateCalls)
		})
	}
}

func TestUS048_PreserveSupplierManagedExtraKeys(t *testing.T) {
	account := &Account{Extra: map[string]any{
		SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3, "keep": true,
	}}
	got := PreserveSupplierManagedExtraKeys(account, map[string]any{"operator_note": "x"})
	require.Equal(t, int64(7), got[SupplierSourceIDExtraKey])
	require.Equal(t, 3, got[SupplierDiscountBandExtraKey])
	require.Equal(t, "x", got["operator_note"])
}

type managedAccountAdminRepoFake struct {
	AccountRepository
	account             *Account
	updated             *Account
	created             *Account
	createCalls         int
	deleteCalls         int
	updateCalls         int
	bulkUpdateCalls     int
	updateExtraCalls    int
	setSchedulableCalls int
	boundGroups         map[int64][]int64
}

func newManagedAccountAdminRepoFake() *managedAccountAdminRepoFake {
	return &managedAccountAdminRepoFake{
		account: &Account{
			ID: 41, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			Name: "managed", Status: StatusActive, Concurrency: 1000, Priority: 130,
			Credentials: map[string]any{"api_key": "secret", "base_url": "https://supplier.example/v1"},
			Extra:       map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
		},
		boundGroups: make(map[int64][]int64),
	}
}

func (r *managedAccountAdminRepoFake) GetByID(context.Context, int64) (*Account, error) {
	copyAccount := *r.account
	copyAccount.Extra = cloneSupplierJSONMap(r.account.Extra)
	copyAccount.Credentials = cloneSupplierJSONMap(r.account.Credentials)
	return &copyAccount, nil
}

func (r *managedAccountAdminRepoFake) GetByIDs(context.Context, []int64) ([]*Account, error) {
	account, _ := r.GetByID(context.Background(), r.account.ID)
	return []*Account{account}, nil
}

func (r *managedAccountAdminRepoFake) ExistsByID(context.Context, int64) (bool, error) {
	return true, nil
}

func (r *managedAccountAdminRepoFake) FindByExtraField(context.Context, string, any) ([]Account, error) {
	return nil, nil
}

func (r *managedAccountAdminRepoFake) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}

func (r *managedAccountAdminRepoFake) Update(_ context.Context, account *Account) error {
	r.updateCalls++
	copyAccount := *account
	copyAccount.Extra = cloneSupplierJSONMap(account.Extra)
	copyAccount.Credentials = cloneSupplierJSONMap(account.Credentials)
	r.updated = &copyAccount
	r.account.Name = account.Name
	r.account.Status = account.Status
	r.account.Priority = account.Priority
	r.account.Concurrency = account.Concurrency
	r.account.Notes = account.Notes
	r.account.Credentials = cloneSupplierJSONMap(account.Credentials)
	r.account.Extra = PreserveSupplierManagedExtraKeys(r.account, cloneSupplierJSONMap(account.Extra))
	return nil
}

func (r *managedAccountAdminRepoFake) BulkUpdate(_ context.Context, _ []int64, updates AccountBulkUpdate) (int64, error) {
	r.bulkUpdateCalls++
	if updates.Priority != nil {
		r.account.Priority = *updates.Priority
	}
	return 1, nil
}

func (r *managedAccountAdminRepoFake) BindGroups(_ context.Context, accountID int64, groupIDs []int64) error {
	r.boundGroups[accountID] = append([]int64(nil), groupIDs...)
	return nil
}

func (r *managedAccountAdminRepoFake) ListByGroup(context.Context, int64) ([]Account, error) {
	return nil, nil
}

func (r *managedAccountAdminRepoFake) Create(_ context.Context, account *Account) error {
	r.createCalls++
	copyAccount := *account
	copyAccount.Extra = cloneSupplierJSONMap(account.Extra)
	r.created = &copyAccount
	return nil
}

func (r *managedAccountAdminRepoFake) Delete(context.Context, int64) error {
	r.deleteCalls++
	return nil
}

func (r *managedAccountAdminRepoFake) UpdateExtra(context.Context, int64, map[string]any) error {
	r.updateExtraCalls++
	return nil
}

func (r *managedAccountAdminRepoFake) SetSchedulable(context.Context, int64, bool) error {
	r.setSchedulableCalls++
	return nil
}

type managedAccountGroupRepoFake struct {
	GroupRepository
	ids map[int64]bool
}

func (g *managedAccountGroupRepoFake) ExistsByIDs(_ context.Context, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = g.ids[id]
	}
	return out, nil
}

func (g *managedAccountGroupRepoFake) GetByID(_ context.Context, id int64) (*Group, error) {
	if !g.ids[id] {
		return nil, ErrGroupNotFound
	}
	return &Group{ID: id, Name: "g", Platform: PlatformNewAPI}, nil
}
