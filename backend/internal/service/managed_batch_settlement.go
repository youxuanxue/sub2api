package service

import (
	"context"
	"errors"
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
	BillingTier                   string
	BillingModel                  string
	ServiceTier                   *string
	ReasoningEffort               *string
	RequestedReasoningEffort      *string
	UpstreamResponseModel         string
	UpstreamResponseModelConflict bool
	InboundEndpoint               string
	UpstreamEndpoint              string
	Usage                         OpenAIUsage
	Subscription                  *UserSubscription
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

	// Charge the authorization frozen before dispatch, never whichever subscription
	// happens to be active after the provider has returned.
	group := in.APIKey.Group
	sub := in.Subscription
	if group == nil || in.APIKey.GroupID == nil || group.ID != *in.APIKey.GroupID || group.IsSubscriptionType() != (sub != nil) ||
		(sub != nil && (sub.ID <= 0 || sub.UserID != in.APIKey.UserID || sub.GroupID != group.ID)) {
		return errors.New("managed batch settlement requires matching authorized group and subscription")
	}

	// Override ambient job IDs: the existing local:<item key> dedup identity
	// must survive both provider retries and worker redelivery.
	settleCtx := context.WithValue(ctx, ctxkey.RequestID, in.RequestID)
	return s.Gateway.RecordUsage(settleCtx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:                     in.RequestID,
			Model:                         in.Model,
			UpstreamModel:                 in.UpstreamModel,
			BillingModel:                  in.BillingModel,
			BillingTier:                   in.BillingTier,
			ServiceTier:                   in.ServiceTier,
			ReasoningEffort:               in.ReasoningEffort,
			RequestedReasoningEffort:      in.RequestedReasoningEffort,
			UpstreamResponseModel:         in.UpstreamResponseModel,
			UpstreamResponseModelConflict: in.UpstreamResponseModelConflict,
			Stream:                        false,
			Usage:                         in.Usage,
		},
		APIKey:             in.APIKey,
		User:               user,
		Account:            in.Account,
		Subscription:       sub,
		InboundEndpoint:    in.InboundEndpoint,
		UpstreamEndpoint:   in.UpstreamEndpoint,
		APIKeyService:      s.APIKeyService,
		ChannelUsageFields: CandidateChannelMapping(ctx, ChannelMappingResult{}).ToUsageFields(in.Model, in.UpstreamModel),
		QuotaPlatform:      QuotaPlatform(settleCtx, in.APIKey),
	})
}

var _ ManagedBatchSettlement = (*OpenAIManagedBatchSettlement)(nil)
