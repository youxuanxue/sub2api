package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// updateUserBalanceWithLedgerTx applies the balance mutation and redeem_codes
// journal row in one transaction so admin recharges stay auditable.
func (s *adminServiceImpl) updateUserBalanceWithLedgerTx(
	ctx context.Context,
	userID int64,
	balance float64,
	operation string,
	notes string,
) (*User, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	opCtx := dbent.NewTxContext(ctx, tx)

	var (
		change BalanceChange
		opErr  error
	)
	switch operation {
	case "set":
		change, opErr = s.userRepo.SetBalance(opCtx, userID, balance)
	case "add":
		change, opErr = s.userRepo.AdjustBalance(opCtx, userID, balance)
	case "subtract":
		change, opErr = s.userRepo.AdjustBalance(opCtx, userID, -balance)
	default:
		return nil, fmt.Errorf("unsupported balance operation: %q", operation)
	}
	if errors.Is(opErr, ErrBalanceNegative) {
		return nil, fmt.Errorf("balance cannot be negative, current balance: %.2f, requested operation would result in: %.2f", change.Old, change.New)
	}
	if opErr != nil {
		return nil, opErr
	}

	balanceDiff := change.New - change.Old
	if balanceDiff != 0 {
		if err := writeBalanceGrantLedger(opCtx, tx.Client(), userID, balanceDiff, notes); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if s.authCacheInvalidator != nil && balanceDiff != 0 {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	s.tryAccrueAffiliateRebateForAdminRecharge(ctx, userID, operation, balance)
	s.invalidateUserBalanceCacheAsync(userID)

	return user, nil
}

func (s *adminServiceImpl) invalidateUserBalanceCacheAsync(userID int64) {
	if s.billingCacheService == nil {
		return
	}
	go func() {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.billingCacheService.InvalidateUserBalance(cacheCtx, userID); err != nil {
			logger.LegacyPrintf("service.admin", "invalidate user balance cache failed: user_id=%d err=%v", userID, err)
		}
	}()
}
