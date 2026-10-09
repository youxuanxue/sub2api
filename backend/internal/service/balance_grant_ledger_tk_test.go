//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsGiftBalanceGrantNote(t *testing.T) {
	require.True(t, IsGiftBalanceGrantNote(BalanceGrantNoteSignup))
	require.True(t, IsGiftBalanceGrantNote(BalanceGrantNoteInviteTrial))
	require.True(t, IsGiftBalanceGrantNote(BalanceGrantNoteOAuthFirstBind))
	require.False(t, IsGiftBalanceGrantNote(BalanceGrantNoteAdminOpening))
	require.False(t, IsGiftBalanceGrantNote(""))
	require.False(t, IsGiftBalanceGrantNote("线下转账补单"))
	// Operator notes are matched verbatim, just like the panel and backfill.
	require.False(t, IsGiftBalanceGrantNote(" "+BalanceGrantNoteSignup+" "))
}

// CreateUser with a positive opening balance must emit an admin_balance journal
// row tagged as an opening grant, so the credit shows in 充值和并发变动记录 and counts
// toward 总充值 (regression for the silently-missing opening balance).
func TestAdminService_CreateUser_OpeningBalance_WritesLedger(t *testing.T) {
	userRepo := &userRepoStub{nextID: 42}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	svc := &adminServiceImpl{
		userRepo:       userRepo,
		redeemCodeRepo: redeemRepo,
		// entClient nil → exercises the non-transactional best-effort fallback.
	}

	bal := 10000.0
	u, err := svc.CreateUser(context.Background(), &CreateUserInput{
		Email:    "compute@tk.com",
		Password: "pw-12345678",
		Balance:  &bal,
	})
	require.NoError(t, err)
	require.Equal(t, int64(42), u.ID)

	require.Len(t, redeemRepo.created, 1)
	rec := redeemRepo.created[0]
	require.Equal(t, AdjustmentTypeAdminBalance, rec.Type)
	require.Equal(t, 10000.0, rec.Value)
	require.Equal(t, StatusUsed, rec.Status)
	require.NotNil(t, rec.UsedBy)
	require.Equal(t, int64(42), *rec.UsedBy)
	require.Equal(t, BalanceGrantNoteAdminOpening, rec.Notes)
	require.InDelta(t, 10000.0, u.TotalRecharged, 0.0001)
}

// A zero opening balance must NOT create a journal row.
func TestAdminService_CreateUser_ZeroBalance_NoLedger(t *testing.T) {
	userRepo := &userRepoStub{nextID: 43}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	svc := &adminServiceImpl{userRepo: userRepo, redeemCodeRepo: redeemRepo}

	bal := 0.0
	_, err := svc.CreateUser(context.Background(), &CreateUserInput{
		Email:    "zero@tk.com",
		Password: "pw-12345678",
		Balance:  &bal,
	})
	require.NoError(t, err)
	require.Empty(t, redeemRepo.created)
}

// The admin recharge path keeps the operator-supplied note as the source tag
// (it is not overwritten with an auto grant tag).
func TestAdminService_UpdateUserBalance_KeepsOperatorNote(t *testing.T) {
	userRepo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 9, Balance: 0}}}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	svc := &adminServiceImpl{userRepo: userRepo, redeemCodeRepo: redeemRepo}

	user, err := svc.UpdateUserBalance(context.Background(), 9, 500, "add", "线下转账补单")
	require.NoError(t, err)
	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, "线下转账补单", redeemRepo.created[0].Notes)
	require.Equal(t, 500.0, redeemRepo.created[0].Value)
	require.InDelta(t, 500.0, user.TotalRecharged, 0.0001)
}

func TestAuthService_CreateUserWithSignupLedger_NilEntClient_BestEffortLedger(t *testing.T) {
	userRepo := &userRepoStub{nextID: 77}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	svc := &AuthService{
		userRepo:   userRepo,
		redeemRepo: redeemRepo,
	}

	user := &User{
		Email:   "signup-ledger@example.com",
		Balance: 3.5,
		Status:  StatusActive,
		Role:    RoleUser,
	}
	require.NoError(t, user.SetPassword("pw-12345678"))
	require.NoError(t, svc.createUserWithSignupLedger(context.Background(), user))

	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, BalanceGrantNoteSignup, redeemRepo.created[0].Notes)
	require.Equal(t, 3.5, redeemRepo.created[0].Value)
	require.NotNil(t, redeemRepo.created[0].UsedBy)
	require.Equal(t, int64(77), *redeemRepo.created[0].UsedBy)
	// Signup gift must not unlock media via total_recharged (A1).
	require.Zero(t, user.TotalRecharged)
}

func TestTrialProvisionService_CreateTrialUserWithLedger_NilEntClient_BestEffortLedger(t *testing.T) {
	userRepo := &userRepoStub{nextID: 88}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	svc := &TrialProvisionService{
		userRepo:       userRepo,
		redeemCodeRepo: redeemRepo,
	}

	user := &User{
		Email:   "trial-ledger@example.com",
		Balance: 12,
		Status:  StatusActive,
		Role:    RoleUser,
	}
	require.NoError(t, user.SetPassword("pw-12345678"))
	require.NoError(t, svc.createTrialUserWithLedger(context.Background(), user))

	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, BalanceGrantNoteInviteTrial, redeemRepo.created[0].Notes)
	require.Equal(t, 12.0, redeemRepo.created[0].Value)
	require.Zero(t, user.TotalRecharged)
}
