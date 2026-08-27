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
	case protocolrouter.AdapterMessagesToGemini:
		executors.MessagesToGemini = execute
	case protocolrouter.AdapterChatToGemini:
		executors.ChatToGemini = execute
	case protocolrouter.AdapterResponsesToGemini:
		executors.ResponsesToGemini = execute
	case protocolrouter.AdapterGeminiIdentity:
		executors.GeminiIdentity = execute
	}
	return executors
}

func protocolExecutionAccountLoaderForTest(account *Account) ProtocolExecutionAccountLoader {
	return func(context.Context, int64) (*Account, error) {
		if account == nil || account.ProtocolEndpointCapability == nil {
			return nil, ErrProtocolCapabilityNotFound
		}
		clone := *account
		capability := *account.ProtocolEndpointCapability
		clone.ProtocolEndpointCapability = &capability
		return &clone, nil
	}
}

func TestExecuteGeminiProtocolProfileUsesOnlyPlannedProfile(t *testing.T) {
	tests := []struct {
		name            string
		profile         protocolrouter.GeminiEndpointProfile
		wantAntigravity int
		wantVertex      int
		wantValue       string
	}{
		{name: "antigravity", profile: protocolrouter.GeminiEndpointAntigravityCloudCode, wantAntigravity: 1, wantValue: "ag"},
		{name: "vertex", profile: protocolrouter.GeminiEndpointVertexServiceAccount, wantVertex: 1, wantValue: "vertex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			antigravityCalls := 0
			vertexCalls := 0
			value, err := ExecuteGeminiProtocolProfile(
				tt.profile,
				func() (any, error) {
					antigravityCalls++
					return "ag", nil
				},
				func() (any, error) {
					vertexCalls++
					return "vertex", nil
				},
			)
			if err != nil {
				t.Fatalf("ExecuteGeminiProtocolProfile: %v", err)
			}
			if antigravityCalls != tt.wantAntigravity || vertexCalls != tt.wantVertex || value != tt.wantValue {
				t.Fatalf("calls/value = antigravity:%d vertex:%d value:%v", antigravityCalls, vertexCalls, value)
			}
		})
	}
}

func TestExecuteGeminiProtocolProfileRejectsUnknownBeforeTransport(t *testing.T) {
	calls := 0
	transport := func() (any, error) {
		calls++
		return nil, nil
	}
	_, err := ExecuteGeminiProtocolProfile(protocolrouter.GeminiEndpointNone, transport, transport)
	if !errors.Is(err, ErrProtocolRouteUnavailable) {
		t.Fatalf("error = %v, want ErrProtocolRouteUnavailable", err)
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d, want 0", calls)
	}
}

func TestProtocolExecutionRouterInvokesGeminiIdentityExecutor(t *testing.T) {
	router := NewProtocolRouter()
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolGeminiGenerateContent,
		RequestedModel:  "client-model",
		Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body:            []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	account := &Account{
		ID:       501,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "secret",
			"project_id":    "project-a",
			"model_mapping": map[string]any{"client-model": "gemini-2.5-pro"},
		},
		Extra: map[string]any{},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolGeminiGenerateContent)
	snapshot, err := protocolAccountSnapshotForRequest(account, request)
	if err != nil {
		t.Fatalf("protocolAccountSnapshotForRequest: %v", err)
	}
	plan, err := router.Plan(request, snapshot)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	calls := 0
	ctx := WithProtocolExecutors(context.Background(), ProtocolExecutors{
		GeminiIdentity: func(_ context.Context, _ *Account, gotPlan protocolrouter.Plan, gotRequest protocolrouter.CanonicalRequest) (any, error) {
			calls++
			if gotPlan.GeminiProfile() != protocolrouter.GeminiEndpointAntigravityCloudCode {
				t.Fatalf("profile = %q", gotPlan.GeminiProfile())
			}
			if gotRequest.Digest() != request.Digest() {
				t.Fatal("executor received different request")
			}
			return "sent", nil
		},
	})
	ctx = withProtocolExecutionAccount(ctx, account)
	ctx = protocolrouter.WithExecutionAccountState(ctx, protocolrouter.ExecutionAccountState{
		AccountID: snapshot.AccountID(), Revision: snapshot.Revision(), CapabilityKey: snapshot.CapabilityKey(), CapabilityRevision: snapshot.CapabilityRevision(), CredentialPresent: true,
	})
	result, err := router.Execute(ctx, plan, request)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 1 || result.Value != "sent" {
		t.Fatalf("calls/result = %d/%v", calls, result.Value)
	}
}

func TestProtocolExecutionRouterInvokesOnlyPlannedAdapterExecutor(t *testing.T) {
	router := NewProtocolRouter()
	req := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	sourceAccount := protocolRoutingOpenAIAccount(12, "responses")
	account, err := ProtocolAccountSnapshot(sourceAccount, "gpt-5.4")
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
		MessagesIdentity: func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
			wrongCalls++
			return nil, nil
		},
		MessagesToResponses: func(
			_ context.Context,
			account *Account,
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
	executionCtx = withProtocolExecutionAccount(executionCtx, sourceAccount)
	executionCtx = protocolrouter.WithExecutionAccountState(executionCtx, protocolrouter.ExecutionAccountState{
		AccountID:          account.AccountID(),
		Revision:           account.Revision(),
		CapabilityKey:      account.CapabilityKey(),
		CapabilityRevision: account.CapabilityRevision(),
		CredentialPresent:  true,
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
		AccountID:          account.AccountID(),
		Revision:           account.Revision(),
		CapabilityKey:      account.CapabilityKey(),
		CapabilityRevision: account.CapabilityRevision(),
		CredentialPresent:  true,
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

func TestOpenAIForwardResultFromForwardPreservesRouteBillingFacts(t *testing.T) {
	effort := "high"
	tier := "priority"
	got := OpenAIForwardResultFromForward(&ForwardResult{
		RequestID:                     "req_gemini",
		Usage:                         ClaudeUsage{InputTokens: 13, OutputTokens: 8, CacheCreationInputTokens: 3, CacheReadInputTokens: 5, ImageOutputTokens: 21},
		Model:                         "client-model",
		UpstreamModel:                 "wire-model",
		UpstreamResponseModel:         "wire-response-model",
		UpstreamResponseModelConflict: true,
		Stream:                        true,
		FirstTokenMs:                  func() *int { value := 12; return &value }(),
		ReasoningEffort:               &effort,
		ServiceTier:                   &tier,
		ImageCount:                    2,
		SearchCount:                   4,
	})
	if got == nil || got.RequestID != "req_gemini" || got.Model != "client-model" || got.UpstreamModel != "wire-model" {
		t.Fatalf("converted result identity = %#v", got)
	}
	if got.Usage.InputTokens != 13 || got.Usage.OutputTokens != 8 || got.Usage.CacheCreationInputTokens != 3 ||
		got.Usage.CacheReadInputTokens != 5 || got.Usage.ImageOutputTokens != 21 {
		t.Fatalf("converted usage = %#v", got.Usage)
	}
	if got.ReasoningEffort != &effort || got.ServiceTier != &tier || got.ImageCount != 2 || got.SearchCount != 4 ||
		!got.UpstreamResponseModelConflict || !got.Stream || got.FirstTokenMs == nil || *got.FirstTokenMs != 12 {
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
		protocolExecutionAccountLoaderForTest(account),
		protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
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
	authoritative := *account
	authoritative.Credentials = map[string]any{
		"api_key":  "changed-secret",
		"base_url": "https://relay.example.test/v1",
	}
	calls := 0

	execute := func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
		calls++
		return nil, nil
	}
	_, err = ExecuteSelectedProtocol(ctx, router, selection, account, func(context.Context, *Account, string) error { return nil }, func(context.Context, int64) (*Account, error) {
		return &authoritative, nil
	}, protocolExecutorsForTest(plan, execute))
	if !errors.Is(err, protocolrouter.ErrStalePlan) {
		t.Fatalf("ExecuteSelectedProtocol error = %v, want ErrStalePlan", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteSelectedProtocolFailsOverMissingAuthorizationBeforeExecutor(t *testing.T) {
	router := NewProtocolRouter()
	request := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, request)
	account := protocolRoutingOpenAIAccount(13, "responses")
	delete(account.Credentials, "api_key")
	plan, _, err := protocolPlanForAccount(ctx, account, request.RequestedModel())
	if err != nil {
		t.Fatalf("protocolPlanForAccount: %v", err)
	}
	calls := 0

	_, err = ExecuteSelectedProtocol(
		ctx,
		router,
		&AccountSelectionResult{Account: account, ProtocolPlan: &plan},
		account,
		func(context.Context, *Account, string) error { return nil },
		protocolExecutionAccountLoaderForTest(account),
		protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
			calls++
			return nil, nil
		}),
	)
	var failover *UpstreamFailoverError
	if !errors.As(err, &failover) || !failover.ShouldRetryNextAccount() {
		t.Fatalf("ExecuteSelectedProtocol error = %v, want next-account failover", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls = %d, want 0 before credential/network boundary", calls)
	}
}

func TestExecuteSelectedProtocolPassesAuthoritativeAccountToExecutor(t *testing.T) {
	router := NewProtocolRouter()
	request := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, request)
	account := protocolRoutingOpenAIAccount(12, "responses")
	plan, _, err := protocolPlanForAccount(ctx, account, request.RequestedModel())
	if err != nil {
		t.Fatalf("protocolPlanForAccount: %v", err)
	}
	authoritative := *account
	authoritative.Credentials = map[string]any{
		"api_key":  account.GetCredential("api_key"),
		"base_url": account.GetCredential("base_url"),
	}
	calls := 0

	_, err = ExecuteSelectedProtocol(
		ctx,
		router,
		&AccountSelectionResult{Account: account, ProtocolPlan: &plan},
		account,
		func(context.Context, *Account, string) error { return nil },
		func(context.Context, int64) (*Account, error) { return &authoritative, nil },
		protocolExecutorsForTest(plan, func(_ context.Context, executionAccount *Account, _ protocolrouter.Plan, _ protocolrouter.CanonicalRequest) (any, error) {
			calls++
			if executionAccount != &authoritative {
				t.Fatalf("executor account = %p, want authoritative %p", executionAccount, &authoritative)
			}
			return "sent", nil
		}),
	)
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
}

func TestExecuteSelectedProtocolFailsClosedForGovernedAccountWithoutSelectedPlan(t *testing.T) {
	router := NewProtocolRouter()
	req := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, req)
	account := protocolRoutingOpenAIAccount(12, "messages")
	calls := 0

	execute := func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
		calls++
		return nil, nil
	}
	_, err := ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account}, account, func(context.Context, *Account, string) error { return nil }, nil, ProtocolExecutors{MessagesIdentity: execute})
	if !errors.Is(err, ErrProtocolRouteUnavailable) {
		t.Fatalf("ExecuteSelectedProtocol error = %v, want ErrProtocolRouteUnavailable", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestExecuteSelectedProtocolUsesLegacyExecutorWhenCutoverRouterDisabled(t *testing.T) {
	request := protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions)
	ctx := WithProtocolRouting(context.Background(), nil, request)
	account := protocolRoutingOpenAIAccount(60, "chat_completions")
	account.Platform = PlatformNewAPI
	selection := &AccountSelectionResult{Account: account}
	calls := 0

	value, err := ExecuteSelectedProtocol(
		ctx,
		nil,
		selection,
		account,
		nil,
		nil,
		ProtocolExecutors{NonGoverned: func(_ context.Context, _ *Account, _ protocolrouter.Plan, got protocolrouter.CanonicalRequest) (any, error) {
			calls++
			if string(got.Body()) != string(request.Body()) {
				t.Fatalf("legacy request body = %q, want %q", got.Body(), request.Body())
			}
			return "legacy", nil
		}},
	)

	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if value != "legacy" || calls != 1 {
		t.Fatalf("value=%v calls=%d, want legacy/1", value, calls)
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
	}, protocolExecutionAccountLoaderForTest(account), protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
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

func TestExecuteSelectedProtocolReloadsAuthoritativeCapabilityBeforeExecutor(t *testing.T) {
	router := NewProtocolRouter()
	request := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, request)
	account := protocolRoutingOpenAIAccount(12, "responses")
	plan, _, err := protocolPlanForAccount(ctx, account, request.RequestedModel())
	if err != nil {
		t.Fatalf("protocolPlanForAccount: %v", err)
	}
	calls := 0
	authoritative := *account.ProtocolEndpointCapability
	authoritative.Revision++
	_, err = ExecuteSelectedProtocol(
		ctx,
		router,
		&AccountSelectionResult{Account: account, ProtocolPlan: &plan},
		account,
		func(context.Context, *Account, string) error { return nil },
		func(context.Context, int64) (*Account, error) {
			fresh := *account
			fresh.ProtocolEndpointCapability = &authoritative
			return &fresh, nil
		},
		protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
			calls++
			return nil, nil
		}),
	)
	if !errors.Is(err, protocolrouter.ErrStalePlan) {
		t.Fatalf("ExecuteSelectedProtocol error = %v, want ErrStalePlan", err)
	}
	if calls != 0 {
		t.Fatalf("executor calls = %d, want 0", calls)
	}
}

func TestProtocolCredentialPresentUsesSanitizedSchedulerSnapshot(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"__tk_protocol_authorization_present": true,
		},
	}
	if !ProtocolAuthorizationPresent(account) {
		t.Fatal("sanitized scheduler snapshot lost positive authorization readiness")
	}

	account.Credentials["__tk_protocol_authorization_present"] = false
	if ProtocolAuthorizationPresent(account) {
		t.Fatal("sanitized scheduler snapshot admitted missing authorization")
	}
}
