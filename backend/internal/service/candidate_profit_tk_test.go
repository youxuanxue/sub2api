//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestCandidateProfitNativeMessagesRejectsBeforeReservation(t *testing.T) {
	group := gatewayProfitTestGroup(10, PlatformAnthropic)
	account := globalCandidateAccount(1, 1, group.ID)
	account.Platform = PlatformAnthropic
	model := "claude-sonnet-4-6"
	account.Credentials["model_mapping"] = map[string]any{model: model}
	attachTestProtocolCapability(&account, protocolrouter.ProtocolMessages)
	account.RateMultiplier = ptrF(0.9)
	r, _, key := globalCandidateFixture([]Group{*group}, []Account{account})
	_, _, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", model,
		[]byte(`{"model":"claude-sonnet-4-6","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`), "", "")
	require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
}

func TestCandidateProfitReselectionUsesActualBillingOrigin(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disabled], func(t *testing.T) {
			first := gatewayProfitTestGroup(10, PlatformOpenAI)
			second := gatewayProfitTestGroup(20, PlatformAnthropic)
			second.RateMultiplier = 0.8
			second.ProfitControlEnabled = !disabled
			accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 20)}
			accounts[0].RateMultiplier, accounts[1].RateMultiplier = ptrF(0.4), ptrF(0.7)
			r, _, key := globalCandidateFixture([]Group{*first, *second}, accounts)
			ctx, state := prepareGlobalCandidate(t, r, key)
			require.Equal(t, int64(10), state.current.group.ID)
			ctx, at := WithGatewayTokenRequestPricing(ctx)
			ctx, openAIAt := r.candidateOpenAI.WithOpenAIRequestPricingContext(ctx, key.GroupID)
			require.Equal(t, at, openAIAt)
			result, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", map[int64]struct{}{1: {}}, "", key.UserID)
			require.NoError(t, err)
			defer result.ReleaseFunc()
			require.Equal(t, int64(20), *key.GroupID)
			admissionCtx := ContextWithSelectionProfitGate(ctx, result)
			gate, _ := admissionCtx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
			if disabled {
				require.Nil(t, gate, "disabled origin must clear the previous candidate's gate")
			} else {
				require.Equal(t, int64(20), gate.groupID)
				require.Equal(t, at, gate.pricingAt)
				require.InDelta(t, 0.8, gate.threshold, 1e-12)
			}
			_, vetoed, _ := r.candidateGateway.GatewayProfitControlVetoLatest(admissionCtx, result.Account)
			require.False(t, vetoed)
		})
	}
}

func TestCandidateProfitFreshCostAfterSlotReleasesAndReselects(t *testing.T) {
	group := gatewayProfitTestGroup(10, PlatformOpenAI)
	accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 10)}
	accounts[0].RateMultiplier, accounts[1].RateMultiplier = ptrF(0.4), ptrF(0.4)
	r, repo, key := globalCandidateFixture([]Group{*group}, accounts)
	var released []int64
	r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{releasedIDs: &released})
	ctx, _ := prepareGlobalCandidate(t, r, key)
	repo.fresh = func(a *Account) *Account {
		copy := *a
		if a.ID == 1 {
			copy.RateMultiplier = ptrF(0.9)
		}
		return &copy
	}
	result, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", nil, "", key.UserID)
	require.NoError(t, err)
	defer result.ReleaseFunc()
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, []int64{1}, released)
	require.True(t, result.ProfitGateActive())
	result.Account.RateMultiplier = ptrF(0.9)
	_, vetoed, _ := r.candidateGateway.GatewayProfitControlVetoLatest(ContextWithSelectionProfitGate(ctx, result), result.Account)
	require.True(t, vetoed, "the handler must receive the same gate as candidate selection")
}

func TestCandidateProfitWaitRechecksFreshCost(t *testing.T) {
	group := gatewayProfitTestGroup(10, PlatformOpenAI)
	account := globalCandidateAccount(1, 1, 10)
	account.RateMultiplier = ptrF(0.4)
	r, repo, key := globalCandidateFixture([]Group{*group}, []Account{account})
	r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 10}},
	})
	ctx, _ := prepareGlobalCandidate(t, r, key)
	result, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", nil, "", key.UserID)
	require.NoError(t, err)
	require.NotNil(t, result.WaitPlan)
	require.True(t, result.ProfitGateActive())
	repo.fresh = func(a *Account) *Account {
		copy := *a
		copy.RateMultiplier = ptrF(0.9)
		return &copy
	}
	require.ErrorIs(t, RecheckCandidateAccountSlot(ctx, account.ID), ErrUniversalCapacityUnavailable)
}

func TestCandidateProfitWebSocketRevalidationRefreshesOriginGate(t *testing.T) {
	group := gatewayProfitTestGroup(10, PlatformOpenAI)
	account := globalCandidateAccount(1, 1, 10)
	account.RateMultiplier = ptrF(0.4)
	account.Extra["openai_apikey_responses_websockets_v2_enabled"] = true
	attachTestProtocolCapability(&account, protocolrouter.ProtocolResponses)
	r, _, key := globalCandidateFixture([]Group{*group}, []Account{account})
	r.candidateOpenAI.cfg = newSchedulerTestOpenAIWSV2Config()
	body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	ctx, state, err := r.PrepareCandidateWebSocket(context.Background(), key, "/v1/responses", "gpt-5.4", body, "", "")
	require.NoError(t, err)
	r.lister.(*stubSpanLister).groups[0].RateMultiplier = 0.2
	r.Invalidate(key.UserID)
	require.ErrorIs(t, state.RevalidateTurn(ctx, account.ID, "gpt-5.4", body), ErrUniversalCapacityUnavailable)
}

func TestCandidateProfitRequestScopeAndTurnRefresh(t *testing.T) {
	group := gatewayProfitTestGroup(10, PlatformAnthropic)
	account := globalCandidateAccount(1, 1, 10)
	account.RateMultiplier = ptrF(0.4)
	r, _, key := globalCandidateFixture([]Group{*group}, []Account{account})
	ctx, state := prepareGlobalCandidate(t, r, key)
	gate, _ := ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	require.NotNil(t, gate)
	state.current.group.RateMultiplier = 0.2
	path := *state.current
	reused := state.withProfitControl(&path)
	require.Same(t, gate, reused.Value(openAIProfitControlGateCtxKey{}), "HTTP retries freeze the same origin's threshold")
	staleGroup := gatewayProfitTestGroup(99, PlatformOpenAI)
	ctx = context.WithValue(ctx, ctxkey.Group, staleGroup)
	ctx = context.WithValue(ctx, openAIPricingAtCtxKey{}, time.Now().Add(-time.Hour))
	turnCtx, turnAt := r.candidateOpenAI.WithOpenAITurnPricingContext(ctx, &staleGroup.ID)
	turnGate, _ := turnCtx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	require.Equal(t, state.current.group.ID, turnGate.groupID)
	require.Equal(t, turnAt, turnGate.pricingAt)
	require.InDelta(t, 0.2, turnGate.threshold, 1e-12)
	_, vetoed, _ := r.candidateOpenAI.ProfitControlVetoLatest(turnCtx, &account)
	require.True(t, vetoed)

	for _, shape := range []UniversalShape{ShapeAnthropicCountTokens, ShapeOpenAIImages, ShapeOpenAIImagesEdit, ShapeOpenAIVideo, ShapeOpenAIAudioSpeech, ShapeOpenAIAudioTranscription} {
		state.shape = shape
		path.ctx = ctx
		require.False(t, gatewayProfitControlGateActive(state.withProfitControl(&path)))
	}
	state.shape = ShapeOpenAIChat
	path.ctx = WithOpenAIProfitControlSuppressed(ctx)
	require.False(t, gatewayProfitControlGateActive(state.withProfitControl(&path)))
}
