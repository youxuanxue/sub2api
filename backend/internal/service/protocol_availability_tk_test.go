//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestProtocolAvailabilityMappedExecutorOutcomes(t *testing.T) {
	for _, governed := range []bool{true, false} {
		t.Run(fmt.Sprint(governed), func(t *testing.T) {
			availability, repo, _ := newAvailabilityTestService(t)
			gateway := &GatewayService{tkPricingAvailability: availability}
			router := NewProtocolRouter()
			request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{InboundProtocol: protocolrouter.ProtocolChatCompletions, RequestedModel: "client-alias", Body: []byte(`{"model":"client-alias","messages":[{"role":"user","content":"hi"}]}`)})
			require.NoError(t, err)
			account := protocolRoutingOpenAIAccount(12, "chat_completions")
			account.Credentials["model_mapping"] = map[string]any{"client-alias": "gpt-5.4"}
			ctx := WithProtocolRouting(context.Background(), router, request)
			plan, _, err := protocolPlanForAccount(ctx, account, request.RequestedModel())
			require.NoError(t, err)
			if !governed {
				account.Type = AccountTypeServiceAccount
			}
			fail := true
			executors := protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
				if fail {
					return nil, fmt.Errorf("wrapped: %w", &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte("unavailable")})
				}
				return &ForwardResult{UpstreamModel: "gpt-5.4"}, nil
			})
			executors.ObserveOutcome = gateway.TKRecordProtocolOutcome
			execute := func() error {
				_, err := ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account, func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(account), executors)
				return err
			}
			require.Error(t, execute())
			state, _ := repo.Get(ctx, account.Platform, "gpt-5.4")
			require.Equal(t, 1, state.SampleTotal24h)
			require.Equal(t, FailureKindUpstream5xx, state.LastFailureKind)
			require.Equal(t, 503, *state.UpstreamStatusCodeLast)
			fail = false
			require.NoError(t, execute())
			state, _ = repo.Get(ctx, account.Platform, "gpt-5.4")
			require.Equal(t, 2, state.SampleTotal24h)
			require.Equal(t, 1, state.SampleOK24h)
			alias, _ := repo.Get(ctx, account.Platform, "client-alias")
			require.Empty(t, alias.ModelID, "request alias and actual upstream model must not create separate evidence cells")
		})
	}
}

func TestProtocolAvailabilityAdmissionAndCancellationHaveNoSamples(t *testing.T) {
	availability, repo, _ := newAvailabilityTestService(t)
	gateway := &GatewayService{tkPricingAvailability: availability}
	account := protocolRoutingOpenAIAccount(12, "responses")
	request := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	router := NewProtocolRouter()
	ctx := WithProtocolRouting(context.Background(), router, request)
	plan, _, err := protocolPlanForAccount(ctx, account, request.RequestedModel())
	require.NoError(t, err)
	executions := 0
	executors := protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
		executions++
		return &ForwardResult{UpstreamModel: "gpt-5.4"}, nil
	})
	executors.ObserveOutcome = gateway.TKRecordProtocolOutcome
	_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account, func(context.Context, *Account, string) error { return errors.New("endpoint changed") }, protocolExecutionAccountLoaderForTest(account), executors)
	require.Error(t, err)
	require.Zero(t, executions)
	for _, local := range []error{errors.New("invalid local request"), fmt.Errorf("wrapped: %w", context.Canceled), &UpstreamFailoverError{Stage: GatewayFailureStageAccountAuth, StatusCode: 503}} {
		gateway.TKRecordProtocolOutcome(ctx, account, plan, request.RequestedModel(), nil, local)
	}
	gateway.TKRecordProtocolOutcome(ctx, account, plan, request.RequestedModel(), (*ForwardResult)(nil), nil)
	require.Empty(t, repo.rows)
}

func TestProtocolAvailabilityPartialOutputCountsOnlyFailure(t *testing.T) {
	availability, repo, _ := newAvailabilityTestService(t)
	gateway := &GatewayService{tkPricingAvailability: availability}
	account := &Account{ID: 12, Platform: PlatformOpenAI}
	for _, err := range []error{io.ErrUnexpectedEOF, errCandidateChatIncomplete, errors.New("partial stream parse error")} {
		gateway.TKRecordProtocolOutcome(context.Background(), account, protocolrouter.Plan{}, "", &ForwardResult{UpstreamModel: "wire-model"}, err)
	}
	state, _ := repo.Get(context.Background(), PlatformOpenAI, "wire-model")
	require.Equal(t, 3, state.SampleTotal24h)
	require.Zero(t, state.SampleOK24h)
	require.Equal(t, FailureKindBadRespShape, state.LastFailureKind)
}

func TestProtocolAvailabilityBillingFallbackDoesNotDoubleCountPartialOrCanceledResult(t *testing.T) {
	for _, forwardErr := range []error{nil, io.ErrUnexpectedEOF, context.Canceled} {
		availability, repo, _ := newAvailabilityTestService(t)
		gateway := &GatewayService{tkPricingAvailability: availability}
		account := &Account{ID: 12, Platform: PlatformOpenAI}
		result := &OpenAIForwardResult{UpstreamModel: "wire-model"}
		gateway.TKRecordProtocolOutcome(context.Background(), account, protocolrouter.Plan{}, "client-model", result, forwardErr)
		converted := ForwardResultFromOpenAI(result)
		gateway.tkRecordAvailabilitySuccessOutcome(context.Background(), account, converted)
		state, _ := repo.Get(context.Background(), PlatformOpenAI, "wire-model")
		if forwardErr == context.Canceled {
			require.Zero(t, state.SampleTotal24h)
		} else {
			require.Equal(t, 1, state.SampleTotal24h)
		}
		if forwardErr != nil {
			require.Zero(t, state.SampleOK24h)
		}
	}
}
