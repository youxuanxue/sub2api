package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func protocolExecutorsForTest(plan protocolrouter.Plan, execute ProtocolExecutionFunc) ProtocolExecutors {
	executors := ProtocolExecutors{NonGoverned: execute}
	switch plan.AdapterID() {
	case protocolrouter.AdapterMessagesIdentity:
		executors.MessagesIdentity = execute
	case protocolrouter.AdapterMessagesToResponses:
		executors.MessagesToResponses = execute
	case protocolrouter.AdapterMessagesToChat:
		executors.MessagesToChat = execute
	case protocolrouter.AdapterChatIdentity:
		executors.ChatIdentity = execute
	case protocolrouter.AdapterChatToResponses:
		executors.ChatToResponses = execute
	case protocolrouter.AdapterChatToMessages:
		executors.ChatToMessages = execute
	case protocolrouter.AdapterResponsesIdentity:
		executors.ResponsesIdentity = execute
	case protocolrouter.AdapterResponsesToChat:
		executors.ResponsesToChat = execute
	case protocolrouter.AdapterResponsesToMessages:
		executors.ResponsesToMessages = execute
	}
	return executors
}

func TestProtocolExecutionRouterInvokesOnlyPlannedAdapterExecutor(t *testing.T) {
	router := NewProtocolRouter()
	req := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	account, err := ProtocolAccountSnapshot(protocolRoutingOpenAIAccount(12, "responses"), "gpt-5.4")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot: %v", err)
	}
	plan, err := router.Plan(req, account)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	calls := 0
	wrongCalls := 0
	executionCtx := WithProtocolExecutors(context.Background(), ProtocolExecutors{
		MessagesIdentity: func(context.Context, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
			wrongCalls++
			return nil, nil
		},
		MessagesToResponses: func(
			_ context.Context,
			gotPlan protocolrouter.Plan,
			gotRequest protocolrouter.CanonicalRequest,
		) (any, error) {
			calls++
			if gotPlan.TargetProtocol() != protocolrouter.ProtocolResponses {
				t.Fatalf("target = %q, want responses", gotPlan.TargetProtocol())
			}
			if gotRequest.Digest() != req.Digest() {
				t.Fatal("executor received a different canonical request")
			}
			return "sent", nil
		},
	})
	executionCtx = protocolrouter.WithExecutionAccountState(executionCtx, protocolrouter.ExecutionAccountState{
		AccountID:         account.AccountID(),
		Revision:          account.Revision(),
		CredentialPresent: true,
	})

	result, err := router.Execute(executionCtx, plan, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Value != "sent" || calls != 1 || wrongCalls != 0 {
		t.Fatalf("result/calls/wrong = %#v/%d/%d", result.Value, calls, wrongCalls)
	}
}

func TestProtocolExecutionRouterFailsBeforeNetworkWhenExecutorIsMissing(t *testing.T) {
	router := NewProtocolRouter()
	req := protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions)
	account, err := ProtocolAccountSnapshot(protocolRoutingOpenAIAccount(12, "chat_completions"), "gpt-5.4")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot: %v", err)
	}
	plan, err := router.Plan(req, account)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ctx := protocolrouter.WithExecutionAccountState(context.Background(), protocolrouter.ExecutionAccountState{
		AccountID:         account.AccountID(),
		Revision:          account.Revision(),
		CredentialPresent: true,
	})

	_, err = router.Execute(ctx, plan, req)
	if !errors.Is(err, ErrProtocolExecutorMissing) {
		t.Fatalf("Execute error = %v, want ErrProtocolExecutorMissing", err)
	}
}

func TestForwardResultFromOpenAIPreservesRouteBillingFacts(t *testing.T) {
	effort := "high"
	tier := "priority"
	got := ForwardResultFromOpenAI(&OpenAIForwardResult{
		RequestID:                     "req_1",
		Usage:                         OpenAIUsage{InputTokens: 7, OutputTokens: 3, CacheCreationInputTokens: 2, CacheReadInputTokens: 5, ImageOutputTokens: 11},
		Model:                         "client-model",
		UpstreamModel:                 "wire-model",
		UpstreamResponseModel:         "wire-response-model",
		UpstreamResponseModelConflict: true,
		Stream:                        true,
		FirstTokenMs:                  func() *int { value := 9; return &value }(),
		ReasoningEffort:               &effort,
		ServiceTier:                   &tier,
		ImageCount:                    1,
		SearchCount:                   2,
	})
	if got == nil || got.RequestID != "req_1" || got.Model != "client-model" || got.UpstreamModel != "wire-model" {
		t.Fatalf("converted result identity = %#v", got)
	}
	if got.Usage.InputTokens != 7 || got.Usage.OutputTokens != 3 || got.Usage.CacheCreationInputTokens != 2 ||
		got.Usage.CacheReadInputTokens != 5 || got.Usage.ImageOutputTokens != 11 {
		t.Fatalf("converted usage = %#v", got.Usage)
	}
	if got.ReasoningEffort != &effort || got.ServiceTier != &tier || got.ImageCount != 1 || got.SearchCount != 2 ||
		!got.UpstreamResponseModelConflict || !got.Stream || got.FirstTokenMs == nil || *got.FirstTokenMs != 9 {
		t.Fatalf("converted route facts = %#v", got)
	}
}

func TestExecuteSelectedProtocolStampsImmutableRouteFactsOnResult(t *testing.T) {
	router := NewProtocolRouter()
	request := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, request)
	account := protocolRoutingOpenAIAccount(12, "responses")
	plan, _, err := protocolPlanForAccount(ctx, account, request.RequestedModel())
	if err != nil {
		t.Fatalf("protocolPlanForAccount: %v", err)
	}
	value, err := ExecuteSelectedProtocol(
		ctx,
		router,
		&AccountSelectionResult{Account: account, ProtocolPlan: &plan},
		account,
		func(context.Context, *Account, string) error { return nil },
		protocolExecutorsForTest(plan, func(context.Context, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
			return &OpenAIForwardResult{}, nil
		}),
	)
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	result, ok := value.(*OpenAIForwardResult)
	if !ok {
		t.Fatalf("result type = %T, want *OpenAIForwardResult", value)
	}
	facts, ok := result.ProtocolRouteFacts()
	if !ok {
		t.Fatal("result is missing route facts")
	}
	if facts.TargetProtocol() != plan.TargetProtocol() || facts.Endpoint() != plan.Endpoint() || facts.ResolvedModel() != plan.ResolvedModel() {
		t.Fatalf("facts = %s/%s/%s, want %s/%s/%s",
			facts.TargetProtocol(), facts.Endpoint(), facts.ResolvedModel(),
			plan.TargetProtocol(), plan.Endpoint(), plan.ResolvedModel())
	}
}

func TestExecuteSelectedProtocolRejectsAccountMutationBeforeExecutor(t *testing.T) {
	router := NewProtocolRouter()
	req := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, req)
	account := protocolRoutingOpenAIAccount(12, "responses")
	plan, _, err := protocolPlanForAccount(ctx, account, "gpt-5.4")
	if err != nil {
		t.Fatalf("protocolPlanForAccount: %v", err)
	}
	selection := &AccountSelectionResult{Account: account, ProtocolPlan: &plan}
	account.Credentials = map[string]any{
		"api_key":  "changed-secret",
		"base_url": "https://relay.example.test/v1",
	}
	calls := 0

	execute := func(context.Context, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
		calls++
		return nil, nil
	}
	_, err = ExecuteSelectedProtocol(ctx, router, selection, account, func(context.Context, *Account, string) error { return nil }, protocolExecutorsForTest(plan, execute))
	if !errors.Is(err, protocolrouter.ErrStalePlan) {
		t.Fatalf("ExecuteSelectedProtocol error = %v, want ErrStalePlan", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteSelectedProtocolFailsClosedForGovernedAccountWithoutSelectedPlan(t *testing.T) {
	router := NewProtocolRouter()
	req := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, req)
	account := protocolRoutingOpenAIAccount(12, "messages")
	calls := 0

	execute := func(context.Context, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
		calls++
		return nil, nil
	}
	_, err := ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account}, account, func(context.Context, *Account, string) error { return nil }, ProtocolExecutors{MessagesIdentity: execute})
	if !errors.Is(err, ErrProtocolRouteUnavailable) {
		t.Fatalf("ExecuteSelectedProtocol error = %v, want ErrProtocolRouteUnavailable", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteSelectedProtocolValidatesExactPlanEndpointBeforeExecutor(t *testing.T) {
	router := NewProtocolRouter()
	req := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, req)
	account := protocolRoutingOpenAIAccount(12, "messages")
	plan, _, err := protocolPlanForAccount(ctx, account, req.RequestedModel())
	if err != nil {
		t.Fatalf("protocolPlanForAccount: %v", err)
	}
	calls := 0

	_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account, func(_ context.Context, _ *Account, endpoint string) error {
		if endpoint != plan.Endpoint() {
			t.Fatalf("validated endpoint = %q, want %q", endpoint, plan.Endpoint())
		}
		return errors.New("blocked by allowlist")
	}, protocolExecutorsForTest(plan, func(context.Context, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
		calls++
		return nil, nil
	}))
	if !errors.Is(err, ErrProtocolRouteUnavailable) {
		t.Fatalf("ExecuteSelectedProtocol error = %v, want ErrProtocolRouteUnavailable", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls = %d, want 0 before credential/network boundary", calls)
	}
}
