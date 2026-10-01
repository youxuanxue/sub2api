package service

import (
	"context"
	"net/http"
	"strings"
)

// TokenKey: unpaid trial media gate. Blocks image/video *submissions* for
// trial-shaped wallets (TotalRecharged<=0 and Balance<=max) so signup bonus
// cannot burn expensive media capacity. Admin-granted high balances pass.
//
// Read path (GET/HEAD video poll) stays open so already-submitted tasks remain
// observable. Kill switch: trial_unpaid_media_blocked=false.

const trialUnpaidMediaDenyMessage = "Image and video generation require a completed recharge. Text models remain available on your trial balance."

// IsTrialUnpaidMediaShape reports whether the universal shape is a media
// generation surface that should be gated for unpaid trials.
func IsTrialUnpaidMediaShape(shape UniversalShape) bool {
	switch shape {
	case ShapeOpenAIImages, ShapeOpenAIImagesEdit, ShapeOpenAIVideo:
		return true
	default:
		return false
	}
}

// IsTrialUnpaidMediaSubmission is true for media shapes that create new work.
// GET/HEAD (video status / content poll) are excluded.
func IsTrialUnpaidMediaSubmission(shape UniversalShape, method string) bool {
	if !IsTrialUnpaidMediaShape(shape) {
		return false
	}
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == http.MethodGet || m == http.MethodHead {
		return false
	}
	return true
}

// TrialUnpaidMediaDecision is the gate outcome for middleware writers.
type TrialUnpaidMediaDecision struct {
	Blocked bool
	Message string
}

// EvaluateTrialUnpaidMedia returns whether the authenticated user may submit
// a media request. settings may be nil (defaults: blocked=true, maxBalance=2).
func EvaluateTrialUnpaidMedia(ctx context.Context, user *User, shape UniversalShape, method string, settings *SettingService) TrialUnpaidMediaDecision {
	if !IsTrialUnpaidMediaSubmission(shape, method) {
		return TrialUnpaidMediaDecision{}
	}
	if user == nil {
		return TrialUnpaidMediaDecision{}
	}
	if user.IsAdmin() {
		return TrialUnpaidMediaDecision{}
	}

	blocked := true
	maxBalance := defaultTrialUnpaidMediaMaxBalanceUSD
	if settings != nil {
		blocked = settings.IsTrialUnpaidMediaBlocked(ctx)
		maxBalance = settings.GetTrialUnpaidMediaMaxBalance(ctx)
	}
	if !blocked {
		return TrialUnpaidMediaDecision{}
	}
	if user.TotalRecharged > 0 {
		return TrialUnpaidMediaDecision{}
	}
	if user.Balance > maxBalance {
		// Ops / admin-granted fat wallets keep TotalRecharged==0 but hold large
		// balances; do not treat them as trial burners.
		return TrialUnpaidMediaDecision{}
	}
	return TrialUnpaidMediaDecision{Blocked: true, Message: trialUnpaidMediaDenyMessage}
}
