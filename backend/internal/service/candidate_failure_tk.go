package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func (r *CandidateRequest) failureCounter() CandidateFailureCounter {
	if r == nil || r.resolver == nil || r.resolver.candidateGateway == nil {
		return nil
	}
	counter, _ := r.resolver.candidateGateway.candidateSaturationState().openai.(CandidateFailureCounter)
	return counter
}

func (r *CandidateRequest) mergeFailureCounts(ctx context.Context, paths []*candidateExecutionPath, counts map[int64]int64) {
	counter := r.failureCounter()
	if counter == nil {
		return
	}
	var scopes []CandidateFailureScope
	for _, path := range paths {
		if path.account.Platform == PlatformNewAPI && path.plan != nil {
			scopes = append(scopes, CandidateFailureScope{path.account.ID, path.plan.ResolvedModel()})
		}
	}
	failures, err := counter.GetCandidateFailures(ctx, scopes)
	if err != nil {
		slog.WarnContext(ctx, "candidate_failure_read_failed", "error", err)
		return
	}
	for scope, count := range failures {
		if count > counts[scope.AccountID] {
			counts[scope.AccountID] = count
		}
	}
}

func candidateFailureAttributable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errCandidateChatIncomplete) {
		return true
	}
	var failure *UpstreamFailoverError
	if !errors.As(err, &failure) || failure.IsCredentialFailure() || failure.RequestScopedTransient || failure.RetryableOnSameAccount || failure.Scope == GatewayFailureScopeRequest || failure.Scope == GatewayFailureScopeProvider {
		return false
	}
	// Auth, explicit quota limits and caller errors retain their dedicated owners.
	return failure.ShouldRetryNextAccount() && failure.StatusCode >= http.StatusInternalServerError && failure.StatusCode != 529
}

func (r *CandidateRequest) observeFailure(ctx context.Context, account *Account, plan protocolrouter.Plan, err error) {
	if account == nil || account.Platform != PlatformNewAPI || ctx.Err() != nil || !candidateFailureAttributable(err) {
		return
	}
	counter := r.failureCounter()
	if counter == nil {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	count, recordErr := counter.IncrementCandidateFailure(recordCtx, CandidateFailureScope{account.ID, plan.ResolvedModel()}, edgeMirrorStubSaturationWindowSeconds)
	if recordErr != nil {
		slog.WarnContext(ctx, "candidate_failure_record_failed", "account_id", account.ID, "error", recordErr)
		return
	}
	if count == edgeMirrorStubSaturationThreshold {
		slog.InfoContext(ctx, "candidate_failure_deprioritized", "account_id", account.ID, "model", plan.ResolvedModel(), "count", count)
	}
}
