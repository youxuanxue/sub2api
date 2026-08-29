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

func TestUS048_ManagedAccountRejectsEveryGenericUpdate(t *testing.T) {
	account := &Account{Extra: map[string]any{SupplierSourceIDExtraKey: int64(7)}}

	require.ErrorIs(t, ValidateSupplierManagedAccountUpdate(account), ErrSupplierManagedAccountProtected)
}

func TestUS048_OrdinaryCreateRejectsSupplierReservedExtra(t *testing.T) {
	for _, key := range []string{SupplierSourceIDExtraKey, SupplierDiscountBandExtraKey} {
		err := ValidateSupplierReservedAccountExtra(map[string]any{key: 7})
		require.ErrorIs(t, err, ErrSupplierReservedAccountExtra)
	}
	require.NoError(t, ValidateSupplierReservedAccountExtra(map[string]any{"operator_note": "ok"}))
}

func TestUS048_ManagedAccountRejectsDirectExtraUpdateDeleteDuplicateAndSchedulable(t *testing.T) {
	repo := newManagedAccountAdminRepoFake()
	svc := &adminServiceImpl{accountRepo: repo}

	err := svc.UpdateAccountExtra(context.Background(), repo.account.ID, map[string]any{"operator_note": "changed"})
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)
	require.Zero(t, repo.updateExtraCalls)

	err = svc.DeleteAccount(context.Background(), repo.account.ID)
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)
	require.Zero(t, repo.deleteCalls)

	_, err = svc.DuplicateAccount(context.Background(), repo.account.ID, "admin:1", "")
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)

	_, err = svc.SetAccountSchedulable(context.Background(), repo.account.ID, true)
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)
	require.Zero(t, repo.setSchedulableCalls)

	_, err = svc.RefreshAccountCredentials(context.Background(), repo.account.ID)
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)
}

func TestUS048_ManagedAccountRejectsGenericSparkShadowCreation(t *testing.T) {
	repo := newManagedAccountAdminRepoFake()
	repo.account.Platform = PlatformOpenAI
	repo.account.Type = AccountTypeOAuth
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.CreateShadow(context.Background(), repo.account.ID, ShadowOptions{Name: "managed-shadow"})

	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)
	require.Zero(t, repo.createCalls)
}

func TestUS048_ManagedAccountRejectsNameOnlyBulkUpdate(t *testing.T) {
	repo := newManagedAccountAdminRepoFake()
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{repo.account.ID}, Name: "renamed",
	})

	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)
	require.Zero(t, repo.updateCalls)
}

func TestUS048_GenericAccountServiceUsesTheSameSupplierOwnershipGuard(t *testing.T) {
	repo := newManagedAccountAdminRepoFake()
	svc := NewAccountService(repo, nil)
	name := "renamed"
	status := StatusDisabled

	_, err := svc.Update(context.Background(), repo.account.ID, UpdateAccountRequest{Name: &name})
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)

	err = svc.UpdateStatus(context.Background(), repo.account.ID, status, "operator disabled")
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)

	err = svc.Delete(context.Background(), repo.account.ID)
	require.ErrorIs(t, err, ErrSupplierManagedAccountProtected)

	_, err = svc.Create(context.Background(), CreateAccountRequest{
		Name: "forged supplier account", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{SupplierSourceIDExtraKey: int64(7)},
	})
	require.ErrorIs(t, err, ErrSupplierReservedAccountExtra)

	require.Zero(t, repo.createCalls)
	require.Zero(t, repo.updateCalls)
	require.Zero(t, repo.deleteCalls)
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
		})
	}
}

type managedAccountAdminRepoFake struct {
	AccountRepository
	account             *Account
	createCalls         int
	deleteCalls         int
	updateCalls         int
	updateExtraCalls    int
	setSchedulableCalls int
}

func newManagedAccountAdminRepoFake() *managedAccountAdminRepoFake {
	return &managedAccountAdminRepoFake{account: &Account{
		ID: 41, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "secret"},
		Extra:       map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
	}}
}

func (r *managedAccountAdminRepoFake) GetByID(context.Context, int64) (*Account, error) {
	copyAccount := *r.account
	copyAccount.Extra = cloneSupplierJSONMap(r.account.Extra)
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

func (r *managedAccountAdminRepoFake) Update(context.Context, *Account) error {
	r.updateCalls++
	return nil
}

func (r *managedAccountAdminRepoFake) Create(context.Context, *Account) error {
	r.createCalls++
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
