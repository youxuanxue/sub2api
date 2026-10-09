package service

import (
	"context"
	"slices"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: unified balance-change journal writer.
//
// The "用户充值和并发变动记录" admin panel lists redeem_codes rows; 总充值 uses
// SumPositiveBalanceByUser (qualifying filter A1/B1 — excludes signup/invite/oauth
// gift notes). users.total_recharged is kept in lockstep on qualifying writes.
// Historically several paths granted balance WITHOUT a journal row; writeBalanceGrantLedger
// closes that gap with one shared, transaction-aware journal writer.
//
// Pass a transaction-bound client (tx.Client()) so the journal row commits and
// rolls back atomically with the balance mutation at the call site. The recorded
// row mirrors the admin recharge shape (type admin_balance, status used), so it
// shows in the panel as a 余额充值（管理员）entry; qualifying credits count toward 总充值.

// Balance-grant source tags carried in the redeem_code notes field so an operator
// can tell paid/admin recharges apart from automatic system grants in one panel.
const (
	BalanceGrantNoteAdminOpening   = "开户期初余额（管理员）"
	BalanceGrantNoteInviteTrial    = "邀请试用赠予"
	BalanceGrantNoteSignup         = "注册初始余额"
	BalanceGrantNoteOAuthFirstBind = "OAuth首次绑定默认余额"
)

// GiftBalanceGrantNotes are automatic trial/signup credits. They appear in
// redeem_codes history but must NOT count toward 总充值 / users.total_recharged
// (media gate + admin panel SSOT: docs/approved/trial-media-recharge-ux-and-total-recharged.md).
func GiftBalanceGrantNotes() []string {
	return []string{
		BalanceGrantNoteSignup,
		BalanceGrantNoteInviteTrial,
		BalanceGrantNoteOAuthFirstBind,
	}
}

// IsGiftBalanceGrantNote reports whether notes tags an automatic gift that is
// excluded from qualifying recharge totals (A1).
func IsGiftBalanceGrantNote(notes string) bool {
	return slices.Contains(GiftBalanceGrantNotes(), notes)
}

// addQualifyingTotalRecharged bumps users.total_recharged for positive,
// non-gift balance grants. No-op for gifts, non-positive amounts, or nil repo.
func addQualifyingTotalRecharged(ctx context.Context, userRepo UserRepository, userID int64, amount float64, notes string) error {
	if userRepo == nil || amount <= 0 || IsGiftBalanceGrantNote(notes) {
		return nil
	}
	return userRepo.AddTotalRecharged(ctx, userID, amount)
}

// bestEffortBalanceGrantLedger records a balance grant via the redeem-code repository
// without a transaction. Used only when no ent client is wired (unit tests); production
// paths use writeBalanceGrantLedger inside the same tx as the balance mutation.
func bestEffortBalanceGrantLedger(ctx context.Context, redeemCodeRepo RedeemCodeRepository, userRepo UserRepository, userID int64, amount float64, notes string, logComponent string) {
	if redeemCodeRepo == nil || amount == 0 {
		return
	}
	code, err := GenerateRedeemCode()
	if err != nil {
		logger.LegacyPrintf(logComponent, "failed to generate balance grant redeem code: %v", err)
		return
	}
	now := time.Now()
	record := &RedeemCode{
		Code:   code,
		Type:   AdjustmentTypeAdminBalance,
		Value:  amount,
		Status: StatusUsed,
		UsedBy: &userID,
		UsedAt: &now,
		Notes:  notes,
	}
	if err := redeemCodeRepo.Create(ctx, record); err != nil {
		logger.LegacyPrintf(logComponent, "failed to create balance grant redeem code: %v", err)
		return
	}
	if err := addQualifyingTotalRecharged(ctx, userRepo, userID, amount, notes); err != nil {
		logger.LegacyPrintf(logComponent, "failed to add total_recharged for balance grant: %v", err)
	}
}

// writeBalanceGrantLedger records a signed balance delta as a used admin_balance
// redeem_code so it appears in the balance-history panel and the 总充值 aggregate.
// client MUST be the transaction client of the same tx that mutates the balance,
// so the journal row and the balance change are atomic. notes carries the source
// tag (see the BalanceGrantNote* constants) or, for admin recharges, the
// operator-supplied reason.
//
// Qualifying positive grants (A1: not signup/invite/oauth gifts) also bump
// users.total_recharged in the same transaction so the field stays aligned with
// SumPositiveBalanceByUser.
func writeBalanceGrantLedger(ctx context.Context, client *dbent.Client, userID int64, amount float64, notes string) error {
	code, err := GenerateRedeemCode()
	if err != nil {
		return err
	}
	_, err = client.RedeemCode.Create().
		SetCode(code).
		SetType(AdjustmentTypeAdminBalance).
		SetValue(amount).
		SetStatus(StatusUsed).
		SetUsedBy(userID).
		SetUsedAt(time.Now()).
		SetNotes(notes).
		Save(ctx)
	if err != nil {
		return err
	}
	if amount <= 0 || IsGiftBalanceGrantNote(notes) {
		return nil
	}
	n, err := client.User.Update().
		Where(user.IDEQ(userID), user.DeletedAtIsNil()).
		AddTotalRecharged(amount).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}
