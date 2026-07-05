package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

var ErrUsageBillingRequestIDRequired = domain.ErrUsageBillingRequestIDRequired
var ErrUsageBillingRequestConflict = domain.ErrUsageBillingRequestConflict

type UsageBillingCommand = domain.UsageBillingCommand

func HashUsageRequestPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// AccountQuotaState holds the post-increment quota state returned by the DB transaction.
// All values are post-update (i.e., already include the increment).
type AccountQuotaState struct {
	TotalUsed   float64
	TotalLimit  float64
	DailyUsed   float64
	DailyLimit  float64
	WeeklyUsed  float64
	WeeklyLimit float64
}

type UsageBillingApplyResult struct {
	Applied              bool
	APIKeyQuotaExhausted bool
	NewBalance           *float64           // post-deduction balance (nil = no balance deduction)
	BalanceOverdrafted   bool               // true when the sufficient-balance guard missed and debt was still recorded
	QuotaState           *AccountQuotaState // post-increment quota state (nil = no quota increment)
}

type UsageBillingRepository interface {
	Apply(ctx context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error)
}
