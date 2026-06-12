package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// TK: terminal-failure refund for async video generations.
//
// Video is billed at submit time (requested duration × per-second price)
// because the submit path holds the full billing context. When the upstream
// later reports a terminal "failed" status at fetch time, the user paid for
// a video that never existed — this companion reverses the charge.
//
// Refund anchor: VideoTaskRecord.BillingRequestID == usage_logs.request_id of
// the original billed row (both sides resolve it from the same submit context
// via TkResolveUsageBillingRequestID). Idempotency: the refund is applied
// through the same usage_billing_dedup table as forward billing, keyed by
// "video-refund:<public_task_id>" — concurrent terminal polls apply at most
// once. Scope: refund returns the user-facing money (balance OR subscription
// usage) and releases api-key quota; time-windowed rate-limit counters and
// upstream account quota are deliberately NOT reversed (refunding into a
// rolling window after the fact is meaningless). Clients that never poll a
// failed task are never refunded — the registry record expires with its TTL;
// the structured logs below are the reconciliation trail.

// TkResolveUsageBillingRequestID resolves the usage-billing request id for the
// current request context — the exact value RecordUsage will persist as
// usage_logs.request_id (the async usage-record worker preserves the relevant
// context keys via usageRecordContext). Handlers stamp it into
// VideoTaskRecord.BillingRequestID and OpenAIForwardResult.RequestID so the
// submit-time billed row stays findable at refund time in every resolution
// branch (ctx-derived or generated).
func TkResolveUsageBillingRequestID(ctx context.Context) string {
	return resolveUsageBillingRequestID(ctx, "")
}

// TkVideoRefundRequestIDPrefix namespaces refund rows in usage_logs.request_id
// and usage_billing_dedup.request_id.
const TkVideoRefundRequestIDPrefix = "video-refund:"

// TkBillingModeVideoRefund marks compensating usage_logs rows written by the
// terminal-failure refund (negative costs; forward video rows carry "video").
const TkBillingModeVideoRefund = "video_refund"

// VideoRefundCommand is the money-movement half of a video refund, applied
// at-most-once by UsageBillingVideoRefundApplier.
type VideoRefundCommand struct {
	RequestID      string // "video-refund:<public_task_id>" — the dedup key
	UserID         int64
	APIKeyID       int64
	SubscriptionID *int64  // non-nil + subscription billing → subscription usage rollback
	BillingType    int8    // BillingTypeBalance / BillingTypeSubscription (from the original row)
	Amount         float64 // > 0, USD returned to the user
}

// UsageBillingVideoRefundApplier is the optional narrow capability the refund
// needs from the usage-billing repository (same pattern as
// usageLogWindowStatsBatchProvider: a type assertion, so test stubs that don't
// care about refunds need no new methods).
type UsageBillingVideoRefundApplier interface {
	ApplyVideoRefund(ctx context.Context, cmd *VideoRefundCommand) (applied bool, err error)
}

// VideoUsageRefundLookupProvider is the optional narrow read capability the
// refund needs from the usage-log repository.
type VideoUsageRefundLookupProvider interface {
	// GetVideoUsageByBillingRequestID returns the newest billing_mode='video'
	// usage row for (request_id, user_id), or (nil, nil) when absent.
	GetVideoUsageByBillingRequestID(ctx context.Context, requestID string, userID int64) (*UsageLog, error)
}

// buildVideoRefundFromUsage derives the refund command + compensating usage
// row from the original billed row. Returns (nil, nil) when there is nothing
// to refund (zero/negative actual cost — unpriced models bill $0 and need no
// reversal). Pure function; unit-tested directly.
func buildVideoRefundFromUsage(rec *VideoTaskRecord, orig *UsageLog) (*VideoRefundCommand, *UsageLog) {
	if rec == nil || orig == nil || orig.ActualCost <= 0 {
		return nil, nil
	}
	refundRequestID := TkVideoRefundRequestIDPrefix + rec.PublicTaskID

	cmd := &VideoRefundCommand{
		RequestID:      refundRequestID,
		UserID:         orig.UserID,
		APIKeyID:       orig.APIKeyID,
		SubscriptionID: orig.SubscriptionID,
		BillingType:    orig.BillingType,
		Amount:         orig.ActualCost,
	}

	billingMode := TkBillingModeVideoRefund
	refundLog := &UsageLog{
		UserID:         orig.UserID,
		APIKeyID:       orig.APIKeyID,
		AccountID:      orig.AccountID,
		RequestID:      refundRequestID,
		Model:          orig.Model,
		RequestedModel: orig.RequestedModel,
		UpstreamModel:  orig.UpstreamModel,
		GroupID:        orig.GroupID,
		SubscriptionID: orig.SubscriptionID,
		BillingType:    orig.BillingType,
		BillingMode:    &billingMode,
		// Double-entry mirror: SUM(total_cost)/SUM(actual_cost) over the pair
		// nets to zero, same for the billed seconds.
		TotalCost:      -orig.TotalCost,
		ActualCost:     -orig.ActualCost,
		RateMultiplier: orig.RateMultiplier,
	}
	if orig.VideoDurationSeconds != nil {
		negSeconds := -*orig.VideoDurationSeconds
		refundLog.VideoDurationSeconds = &negSeconds
	}
	return cmd, refundLog
}

// RefundVideoUsageOnFailure reverses the submit-time charge after the upstream
// reported a terminal failed status. Runs on the usage-record worker pool;
// every outcome is logged (never returns an error to the poll response path).
func (s *OpenAIGatewayService) RefundVideoUsageOnFailure(ctx context.Context, rec *VideoTaskRecord) {
	if s == nil || rec == nil {
		return
	}
	log := logger.L().With(
		zap.String("component", "service.openai_gateway.video_refund"),
		zap.String("public_task_id", rec.PublicTaskID),
		zap.Int64("user_id", rec.UserID),
		zap.Int64("api_key_id", rec.APIKeyID),
		zap.String("model", rec.OriginModel),
	)
	if strings.TrimSpace(rec.BillingRequestID) == "" {
		// Records saved before BillingRequestID existed cannot be refunded
		// automatically — leave a reconciliation trail.
		log.Warn("openai_video_refund.skipped_no_billing_request_id")
		return
	}
	lookup, ok := s.usageLogRepo.(VideoUsageRefundLookupProvider)
	if !ok {
		log.Warn("openai_video_refund.skipped_lookup_unsupported")
		return
	}
	applier, ok := s.usageBillingRepo.(UsageBillingVideoRefundApplier)
	if !ok {
		log.Warn("openai_video_refund.skipped_applier_unsupported")
		return
	}

	// The submit-time RecordUsage runs on the same async worker pool with no
	// ordering guarantee relative to this refund task; a fast terminal poll
	// can race ahead of the billed row landing. Bounded retry turns that
	// narrow window from a permanently lost refund (the registry record is
	// already deleted) into a short wait.
	var orig *UsageLog
	var err error
	for attempt := 1; ; attempt++ {
		orig, err = lookup.GetVideoUsageByBillingRequestID(ctx, rec.BillingRequestID, rec.UserID)
		if err != nil {
			log.Error("openai_video_refund.original_lookup_failed", zap.Error(err))
			return
		}
		if orig != nil || attempt >= 3 {
			break
		}
		select {
		case <-ctx.Done():
			log.Error("openai_video_refund.original_usage_not_found",
				zap.String("billing_request_id", rec.BillingRequestID),
				zap.Int("attempts", attempt), zap.Error(ctx.Err()))
			return
		case <-time.After(2 * time.Second):
		}
	}
	if orig == nil {
		// Unrecoverable from here (registry record already deleted; clients
		// rarely re-poll a failed task) — Error level so reconciliation
		// alerting sees it.
		log.Error("openai_video_refund.original_usage_not_found",
			zap.String("billing_request_id", rec.BillingRequestID),
			zap.Int("attempts", 3))
		return
	}

	cmd, refundLog := buildVideoRefundFromUsage(rec, orig)
	if cmd == nil {
		log.Info("openai_video_refund.nothing_to_refund",
			zap.Float64("original_actual_cost", orig.ActualCost))
		return
	}

	applied, err := applier.ApplyVideoRefund(ctx, cmd)
	if err != nil {
		log.Error("openai_video_refund.apply_failed",
			zap.Float64("amount", cmd.Amount), zap.Error(err))
		return
	}
	if !applied {
		log.Info("openai_video_refund.already_refunded")
		return
	}

	if _, err := s.usageLogRepo.Create(ctx, refundLog); err != nil {
		// Money already returned; the structured log below carries the
		// amounts, so a missing audit row is recoverable by reconciliation.
		log.Error("openai_video_refund.compensation_log_failed", zap.Error(err))
	}

	deps := s.billingDeps()
	if deps != nil && deps.billingCacheService != nil {
		// Invalidate instead of arithmetic: the cache write primitives are
		// only exercised with positive amounts on the forward path; a fresh
		// read from the now-correct DB is the safe way back.
		if cmd.BillingType == BillingTypeSubscription {
			groupID := rec.GroupID
			if orig.GroupID != nil {
				groupID = *orig.GroupID
			}
			_ = deps.billingCacheService.InvalidateSubscription(ctx, cmd.UserID, groupID)
		} else {
			_ = deps.billingCacheService.InvalidateUserBalance(ctx, cmd.UserID)
		}
	}

	log.Info("openai_video_refund.applied",
		zap.Float64("amount", cmd.Amount),
		zap.Int8("billing_type", cmd.BillingType),
		zap.String("billing_request_id", rec.BillingRequestID),
	)
}
