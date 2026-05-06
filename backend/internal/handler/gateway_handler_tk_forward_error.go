package handler

// TokenKey: passive availability failure tap helper.
//
// Rule §5 (CLAUDE.md): keep upstream-shaped handler files thin. Each gateway
// failure tap site (5 today: gateway_handler.go × 2, chat_completions, responses,
// gemini_v1beta) should be a single line; the errors.As extraction lives here.
//
// Why this helper exists (R-004 of
// docs/approved/pricing-availability-source-of-truth.md):
//
// Before: handlers passed statusCode=0 to TKRecordForwardFailure. The
// classifier in pricing_availability_service_tk.go requires UpstreamStatusCode
// to be 4xx to recognize model_not_found bodies; with statusCode=0 a real
// upstream 404 ("Requested entity was not found.") fell into the default soft
// accumulator (upstream_5xx) instead of single-sample → unreachable. The
// strong signal promised by §1.3 of the design never fired in production.
//
// After: TkRecordFailureFromErr unwraps the existing *service.UpstreamFailoverError
// (already used elsewhere by the failover routing logic) and pulls the real
// upstream HTTP status + body. No new error type required — the failover
// error covers ~all gateway error returns that observed a real upstream
// response, which is precisely the population the availability classifier
// cares about.

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TkRecordFailureFromErr is the single tap helper used by every gateway
// failure path. It unwraps service.UpstreamFailoverError (the canonical
// gateway error type that carries upstream HTTP status + body) and forwards
// the real status to TKRecordForwardFailure so the availability classifier
// can apply the §1.3 matrix correctly.
//
// Behavior:
//   - svc nil OR err nil → no-op.
//   - err is *UpstreamFailoverError (any depth via errors.As) → extract
//     StatusCode + ResponseBody, classify the body via the existing
//     classifier in pricing_availability_service_tk.go.
//   - otherwise → fall back to the previous behavior (statusCode=0 +
//     err.Error()), which still routes through the soft accumulator path.
//     Pre-flight / before-forward errors that never observed an upstream
//     response correctly belong in this branch.
//
// nil-safety on svc and on the receiver inside TKRecordForwardFailure means
// callers do not need to gate on availability being wired.
func TkRecordFailureFromErr(
	svc *service.GatewayService,
	ctx context.Context,
	platform string,
	model string,
	accountID int64,
	err error,
) {
	if svc == nil || err == nil {
		return
	}
	statusCode := 0
	body := err.Error()
	network := false

	var foErr *service.UpstreamFailoverError
	if errors.As(err, &foErr) && foErr != nil {
		statusCode = foErr.StatusCode
		// UpstreamFailoverError.ResponseBody is the raw upstream bytes; prefer
		// it over err.Error() because the latter is the formatted "upstream
		// error: 404 (failover)" string that contains no body keywords for
		// classifyFailureKind to match against.
		if len(foErr.ResponseBody) > 0 {
			body = string(foErr.ResponseBody)
		}
	}

	svc.TKRecordForwardFailure(ctx, platform, model, accountID, statusCode, body, network)
}
