package service

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// TokenKey: unified balance-change journal writer.
//
// The "用户充值和并发变动记录" admin panel and the 总充值 (total recharge) figure
// are both derived ONLY from redeem_codes rows (see
// adminServiceImpl.GetUserBalanceHistory / redeemCodeRepository.SumPositiveBalanceByUser).
// Historically several paths granted or adjusted users.balance WITHOUT writing a
// journal row — opening balance at account creation (CreateUser / invite-trial),
// the signup bonus, and OAuth first-bind provider defaults — so those credits were
// invisible in the panel and silently undercounted in 总充值, while the admin
// recharge button wrote its row in a separate, non-atomic step that was lost on
// error. writeBalanceGrantLedger closes that gap by giving every balance-granting
// path one shared, transaction-aware journal writer.
//
// Pass a transaction-bound client (tx.Client()) so the journal row commits and
// rolls back atomically with the balance mutation at the call site. The recorded
// row mirrors the admin recharge shape (type admin_balance, status used), so it
// shows in the panel as a 余额充值（管理员）entry and counts toward 总充值.

// Balance-grant source tags carried in the redeem_code notes field so an operator
// can tell paid/admin recharges apart from automatic system grants in one panel.
const (
	BalanceGrantNoteAdminOpening   = "开户期初余额（管理员）"
	BalanceGrantNoteInviteTrial    = "邀请试用赠予"
	BalanceGrantNoteSignup         = "注册初始余额"
	BalanceGrantNoteOAuthFirstBind = "OAuth首次绑定默认余额"
)

// writeBalanceGrantLedger records a signed balance delta as a used admin_balance
// redeem_code so it appears in the balance-history panel and the 总充值 aggregate.
// client MUST be the transaction client of the same tx that mutates the balance,
// so the journal row and the balance change are atomic. notes carries the source
// tag (see the BalanceGrantNote* constants) or, for admin recharges, the
// operator-supplied reason.
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
	return err
}
