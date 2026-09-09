//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

type candidateFailureTestCounter struct {
	OpenAISaturationCounterCache
	counts map[CandidateFailureScope]int64
	err    error
}

func (c *candidateFailureTestCounter) IncrementCandidateFailure(_ context.Context, scope CandidateFailureScope, _ int) (int64, error) {
	c.counts[scope]++
	return c.counts[scope], nil
}
func (c *candidateFailureTestCounter) GetCandidateFailures(_ context.Context, scopes []CandidateFailureScope) (map[CandidateFailureScope]int64, error) {
	if c.err != nil {
		return nil, c.err
	}
	out := map[CandidateFailureScope]int64{}
	for _, scope := range scopes {
		out[scope] = c.counts[scope]
	}
	return out, nil
}

func TestCandidateFailurePriorityDirectUniversalAndModelIsolation(t *testing.T) {
	for _, direct := range []bool{false, true} {
		group := grp(10, PlatformNewAPI, 1, false)
		accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 10)}
		for i := range accounts {
			accounts[i].Platform = PlatformNewAPI
			accounts[i].ChannelType = 1
			accounts[i].Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-4o"}
			attachTestProtocolCapability(&accounts[i], protocolrouter.ProtocolChatCompletions)
		}
		accounts[0].Concurrency = 1000
		r, _, key := globalCandidateFixture([]Group{group}, accounts)
		counter := &candidateFailureTestCounter{counts: map[CandidateFailureScope]int64{}}
		r.candidateGateway.rateLimitService = &RateLimitService{openaiSaturationCounter: counter}
		if direct {
			key.RoutingMode = "direct"
			key.GroupID = &group.ID
			key.Group = &group
		}
		ctx, state := prepareGlobalCandidate(t, r, key)
		state.session = "existing"
		r.candidateGateway.cache = &stubGatewayCache{sessionBindings: map[string]int64{"existing": 1}}
		require.Equal(t, int64(1), state.current.account.ID)
		plan := *state.current.plan
		require.Equal(t, "gpt-4o", plan.ResolvedModel())
		failure := &UpstreamFailoverError{StatusCode: http.StatusBadGateway, Scope: GatewayFailureScopeAccount}
		for range 2 {
			state.observeFailure(ctx, &accounts[0], plan, failure)
		}
		picked, err := state.selectAccount(ctx, candidateSelectOptions{})
		require.NoError(t, err)
		require.Equal(t, int64(1), picked.Account.ID)
		state.observeFailure(ctx, &accounts[0], plan, failure)
		require.Equal(t, int64(3), counter.counts[CandidateFailureScope{1, "gpt-4o"}])
		require.Zero(t, counter.counts[CandidateFailureScope{1, "gpt-5.4"}])
		picked, err = state.selectAccount(ctx, candidateSelectOptions{})
		require.NoError(t, err)
		require.Equal(t, int64(2), picked.Account.ID, "large capacity cannot rescue the failing priority-1 account")
		state.continuationAccountID = 1
		picked, err = state.selectAccount(ctx, candidateSelectOptions{})
		require.NoError(t, err)
		require.Equal(t, int64(1), picked.Account.ID, "hard continuation cannot migrate due to a soft penalty")
		state.continuationAccountID = 0
		counter.err = errors.New("counter unavailable")
		picked, err = state.selectAccount(ctx, candidateSelectOptions{})
		require.NoError(t, err)
		require.Equal(t, int64(1), picked.Account.ID, "counter outage preserves configured priority")
		counter.err = nil
		counter.counts = map[CandidateFailureScope]int64{{1, "unrelated-model"}: 10}
		picked, err = state.selectAccount(ctx, candidateSelectOptions{})
		require.NoError(t, err)
		require.Equal(t, int64(1), picked.Account.ID)
	}
}

func TestCandidateFailureAttributionDoesNotPenalizeCallerOrDuplicateCooldown(t *testing.T) {
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded,
		&UpstreamFailoverError{StatusCode: 400},
		&UpstreamFailoverError{StatusCode: 429},
		&UpstreamFailoverError{StatusCode: 529},
		&UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeRequest},
		&UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeProvider},
		&UpstreamFailoverError{StatusCode: 503, RequestScopedTransient: true},
		&UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true},
		&UpstreamFailoverError{StatusCode: 503, Stage: GatewayFailureStageAccountAuth},
	} {
		require.False(t, candidateFailureAttributable(err), "%v", err)
	}
	require.True(t, candidateFailureAttributable(&UpstreamFailoverError{StatusCode: 504, Scope: GatewayFailureScopeAccount}))
}
