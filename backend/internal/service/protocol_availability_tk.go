package service

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// TKRecordProtocolOutcome observes one invoked forward executor, independently
// of billing or the handler's retry decision. A partial result with an error is
// one failed attempt, never both a success and a failure. Admission/cancellation
// errors with no upstream evidence do not enter the model-health denominator.
func (s *GatewayService) TKRecordProtocolOutcome(ctx context.Context, account *Account, plan protocolrouter.Plan, requestedModel string, value any, err error) {
	if s == nil || s.tkPricingAvailability == nil || account == nil {
		return
	}
	model := candidateFailureScope(account, plan, CandidateEffectiveModel(ctx, requestedModel)).Model
	hasResult := false
	switch result := value.(type) {
	case *ForwardResult:
		hasResult = result != nil
		if result != nil {
			if result.availabilityObserved {
				return
			}
			result.availabilityObserved = true
		}
		if result != nil && result.UpstreamModel != "" {
			model = result.UpstreamModel
		}
	case *OpenAIForwardResult:
		hasResult = result != nil
		if result != nil {
			if result.availabilityObserved {
				return
			}
			result.availabilityObserved = true
		}
		if result != nil && result.UpstreamModel != "" {
			model = result.UpstreamModel
		}
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	outcome := AvailabilityOutcome{Platform: account.Platform, ModelID: model, AccountID: account.ID}
	if err == nil {
		if !hasResult {
			return
		}
		outcome.Success = true
		outcome.UpstreamStatusCode = 200
	} else {
		var failure *UpstreamFailoverError
		var network net.Error
		var transport *candidateTransportError
		switch {
		case errors.As(err, &failure) && failure != nil && failure.Stage == GatewayFailureStageAccountAuth:
			return // Credential preparation has not observed an inference attempt.
		case errors.Is(err, errCandidateChatIncomplete):
			outcome.UpstreamStatusCode = 200
		case errors.As(err, &failure) && failure != nil && failure.StatusCode > 0:
			outcome.UpstreamStatusCode = failure.StatusCode
			outcome.UpstreamErrorBody = truncateErrorBody(string(failure.ResponseBody))
		case errors.As(err, &network), errors.As(err, &transport), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			outcome.NetworkError = true
		case hasResult:
			// A returned partial result is upstream evidence even when an adapter
			// has not wrapped its stream/response-shape error in a typed error.
			outcome.UpstreamStatusCode = 200
		default:
			return
		}
	}
	// Bound best-effort persistence independently of the response lifetime.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	s.tkPricingAvailability.RecordOutcome(recordCtx, outcome)
}
