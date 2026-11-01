package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// ManagedBatchSettlementInput is the immutable usage snapshot produced by a
// successful managed item. The worker owns the stable request ID; this service
// owns pricing, billing, quota and usage-log persistence through RecordUsage.
type ManagedBatchSettlementInput struct {
	BatchID                       string
	ItemID                        int64
	RequestID                     string
	Model                         string
	UpstreamModel                 string
	BillingModel                  string
	ServiceTier                   *string
	ReasoningEffort               *string
	RequestedReasoningEffort      *string
	UpstreamResponseModel         string
	UpstreamResponseModelConflict bool
	InboundEndpoint               string
	UpstreamEndpoint              string
	InputTokens                   int
	OutputTokens                  int
	CacheReadTokens               int
	APIKey                        *APIKey
	Account                       *Account
}

// ManagedBatchSettlement is intentionally narrow so the worker cannot depend
// on billing internals or accidentally implement a second pricing path.
type ManagedBatchSettlement interface {
	SettleManagedBatch(context.Context, ManagedBatchSettlementInput) error
}

// OpenAIManagedBatchSettlement adapts a successful managed Responses item to
// the existing OpenAI usage/billing owner. It is safe to call again with the
// same input: RecordUsage's request-id and billing dedup keys make retries
// idempotent.
type OpenAIManagedBatchSettlement struct {
	Gateway       *OpenAIGatewayService
	APIKeyService APIKeyQuotaUpdater
}

func (s *OpenAIManagedBatchSettlement) SettleManagedBatch(ctx context.Context, in ManagedBatchSettlementInput) error {
	if s == nil || s.Gateway == nil {
		return errors.New("managed batch settlement unavailable")
	}
	if in.APIKey == nil || in.Account == nil || in.APIKey.UserID <= 0 || in.Account.ID <= 0 {
		return errors.New("managed batch settlement requires api key and account")
	}
	if strings.TrimSpace(in.RequestID) == "" || strings.TrimSpace(in.Model) == "" {
		return errors.New("managed batch settlement requires stable request id and model")
	}
	user := in.APIKey.User
	if user == nil {
		user = &User{ID: in.APIKey.UserID}
	}

	var subscription *UserSubscription
	if in.APIKey.Group != nil && in.APIKey.Group.IsSubscriptionType() {
		if s.Gateway.userSubRepo == nil || in.APIKey.GroupID == nil {
			return errors.New("managed batch subscription repository unavailable")
		}
		var err error
		subscription, err = s.Gateway.userSubRepo.GetActiveByUserIDAndGroupID(ctx, in.APIKey.UserID, *in.APIKey.GroupID)
		if err != nil {
			return fmt.Errorf("load managed batch subscription: %w", err)
		}
		if subscription == nil {
			return errors.New("managed batch active subscription not found")
		}
	}

	// RecordUsage uses the context request ID only as a fallback. Supplying the
	// stable item key directly makes both usage_logs and usage_billing_dedup
	// converge on the same idempotency key across provider retries.
	settleCtx := context.WithValue(ctx, ctxkey.RequestID, in.RequestID)
	return s.Gateway.RecordUsage(settleCtx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:                     in.RequestID,
			Model:                         in.Model,
			UpstreamModel:                 in.UpstreamModel,
			BillingModel:                  in.BillingModel,
			ServiceTier:                   in.ServiceTier,
			ReasoningEffort:               in.ReasoningEffort,
			RequestedReasoningEffort:      in.RequestedReasoningEffort,
			UpstreamResponseModel:         in.UpstreamResponseModel,
			UpstreamResponseModelConflict: in.UpstreamResponseModelConflict,
			Stream:                        false,
			Usage: OpenAIUsage{
				InputTokens:          in.InputTokens,
				OutputTokens:         in.OutputTokens,
				CacheReadInputTokens: in.CacheReadTokens,
			},
		},
		APIKey:           in.APIKey,
		User:             user,
		Account:          in.Account,
		Subscription:     subscription,
		InboundEndpoint:  in.InboundEndpoint,
		UpstreamEndpoint: in.UpstreamEndpoint,
		APIKeyService:    s.APIKeyService,
		QuotaPlatform:    QuotaPlatform(settleCtx, in.APIKey),
	})
}

var _ ManagedBatchSettlement = (*OpenAIManagedBatchSettlement)(nil)
