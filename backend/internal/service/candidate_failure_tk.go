package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

// Keep the sanitized error text while retaining transport failure attribution.
type candidateTransportError struct{ error }

func candidateTransportFailure(ctx context.Context, rendered, cause error) error {
	if CandidateRequestFromContext(ctx) == nil || ctx.Err() != nil || errors.Is(cause, context.Canceled) {
		return rendered
	}
	var networkError net.Error
	if errors.As(cause, &networkError) || errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) {
		return &candidateTransportError{rendered}
	}
	return rendered
}

// Handlers retain error identity; the legacy health breaker must not count it again.
type candidateObservedFailure struct{ error }

func (e *candidateObservedFailure) Unwrap() error { return e.error }

func candidateFailureScope(account *Account, plan protocolrouter.Plan, model string) CandidateFailureScope {
	resolved := plan.ResolvedModel()
	if resolved == "" {
		switch {
		case account.IsBedrock():
			resolved, _ = ResolveBedrockModelID(account, model)
		case account.Platform == PlatformGemini:
			resolved, _ = resolveGeminiForwardModels(account, model)
		case account.Platform == PlatformKiro:
			resolved = kiroproto.MapModel(model)
		default:
			resolved = account.GetMappedModel(model)
		}
	}
	return CandidateFailureScope{account.ID, resolved}
}

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
		var plan protocolrouter.Plan
		if path.plan != nil {
			plan = *path.plan
		}
		scopes = append(scopes, candidateFailureScope(path.account, plan, path.model))
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
	var transport *candidateTransportError
	if errors.As(err, &transport) {
		return true
	}
	var failure *UpstreamFailoverError
	if !errors.As(err, &failure) || failure.IsCredentialFailure() || failure.RequestScopedTransient || failure.RetryableOnSameAccount || failure.Scope == GatewayFailureScopeRequest || failure.Scope == GatewayFailureScopeProvider {
		return false
	}
	// Auth, explicit quota limits and caller errors retain their dedicated owners.
	return failure.ShouldRetryNextAccount() && failure.StatusCode >= http.StatusInternalServerError && failure.StatusCode != 529
}

func (r *CandidateRequest) observeFailure(ctx context.Context, account *Account, plan protocolrouter.Plan, err error) error {
	var observed *candidateObservedFailure
	if account == nil || ctx.Err() != nil || errors.As(err, &observed) || !candidateFailureAttributable(err) {
		return err
	}
	counter := r.failureCounter()
	if counter == nil {
		return err
	}
	model := r.model
	if r.current != nil && r.current.account.ID == account.ID {
		model = r.current.model
	}
	scope := candidateFailureScope(account, plan, model)
	if scope.Model == "" {
		return err
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	count, recordErr := counter.IncrementCandidateFailure(recordCtx, scope, edgeMirrorStubSaturationWindowSeconds)
	if recordErr != nil {
		slog.WarnContext(ctx, "candidate_failure_record_failed", "account_id", account.ID, "error", recordErr)
	} else if count == edgeMirrorStubSaturationThreshold {
		slog.InfoContext(ctx, "candidate_failure_deprioritized", "account_id", account.ID, "model", scope.Model, "count", count)
	}
	return &candidateObservedFailure{err}
}
