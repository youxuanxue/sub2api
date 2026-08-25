package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

type protocolRoutingNoopAdapter struct{}

func (protocolRoutingNoopAdapter) Execute(context.Context, protocolrouter.Execution) (protocolrouter.Result, error) {
	return protocolrouter.Result{}, nil
}

func protocolRoutingTestRouter() *protocolrouter.Router {
	adapter := protocolRoutingNoopAdapter{}
	return protocolrouter.New(protocolrouter.AdapterCatalog{
		protocolrouter.AdapterMessagesIdentity:    adapter,
		protocolrouter.AdapterMessagesToResponses: adapter,
		protocolrouter.AdapterMessagesToChat:      adapter,
		protocolrouter.AdapterChatIdentity:        adapter,
		protocolrouter.AdapterChatToResponses:     adapter,
		protocolrouter.AdapterChatToMessages:      adapter,
		protocolrouter.AdapterResponsesIdentity:   adapter,
		protocolrouter.AdapterResponsesToChat:     adapter,
		protocolrouter.AdapterResponsesToMessages: adapter,
	})
}

func protocolRoutingTestRequest(t *testing.T, protocol protocolrouter.Protocol) protocolrouter.CanonicalRequest {
	return protocolRoutingTestRequestForPath(t, protocol, protocolrouter.ResponsesPathNone)
}

func protocolRoutingTestRequestForPath(
	t *testing.T,
	protocol protocolrouter.Protocol,
	responsesPath protocolrouter.ResponsesPathKind,
) protocolrouter.CanonicalRequest {
	t.Helper()
	req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocol,
		RequestedModel:  "gpt-5.4",
		ResponsesPath:   responsesPath,
		Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body:            []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	return req
}

func TestProtocolRoutingCanonicalFactReplacesLegacyTextCapabilityGate(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequestForPath(t, protocolrouter.ProtocolResponses, protocolrouter.ResponsesPathInputTokens),
	)
	account := protocolRoutingOpenAIAccount(3, "responses")
	account.Credentials["openai_capabilities"] = []any{"embeddings"}

	if !isOpenAICompatibleAccountEligibleForRequest(
		ctx,
		account,
		PlatformOpenAI,
		"gpt-5.4",
		false,
		OpenAIEndpointCapabilityChatCompletions,
	) {
		t.Fatal("legacy text capability overrode canonical supported_protocols")
	}
}

func TestResponsesInputTokensRequiresNativeResponsesRoute(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequestForPath(t, protocolrouter.ProtocolResponses, protocolrouter.ResponsesPathInputTokens),
	)
	if ProtocolRouteLegal(ctx, protocolRoutingOpenAIAccount(4, "chat_completions"), "gpt-5.4") {
		t.Fatal("responses input_tokens converted to chat_completions")
	}
	if !ProtocolRouteLegal(ctx, protocolRoutingOpenAIAccount(5, "responses"), "gpt-5.4") {
		t.Fatal("native responses input_tokens route was rejected")
	}
}

func TestSelectProtocolAccountForTokenCountReturnsSchedulerPlan(t *testing.T) {
	groupID := int64(10116)
	account := *protocolRoutingOpenAIAccount(6, "responses")
	account.Credentials["openai_capabilities"] = []any{"embeddings"}
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequestForPath(t, protocolrouter.ProtocolResponses, protocolrouter.ResponsesPathInputTokens),
	)
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:       &schedulerTestGatewayCache{},
		cfg:         &config.Config{},
	}

	selection, err := svc.SelectProtocolAccountForTokenCount(
		ctx,
		&groupID,
		"",
		"gpt-5.4",
		OpenAIEndpointCapabilityResponses,
		PlatformOpenAI,
	)
	if err != nil {
		t.Fatalf("SelectProtocolAccountForTokenCount: %v", err)
	}
	plan, ok := ProtocolPlanFromSelection(selection)
	if !ok {
		t.Fatal("token-count selection has no protocol plan")
	}
	if plan.AccountID() != account.ID || plan.TargetProtocol() != protocolrouter.ProtocolResponses ||
		plan.ResponsesPath() != protocolrouter.ResponsesPathInputTokens {
		t.Fatalf("selection plan = account %d target %q path %q", plan.AccountID(), plan.TargetProtocol(), plan.ResponsesPath())
	}
}

func protocolRoutingOpenAIAccount(id int64, protocols ...string) *Account {
	extraProtocols := make([]any, len(protocols))
	for i, protocol := range protocols {
		extraProtocols[i] = protocol
	}
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
		Extra: map[string]any{SupportedProtocolsExtraKey: extraProtocols},
	}
}

func TestProtocolRoutingHardGateRejectsGovernedAccountWithoutLegalPath(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages),
	)
	missing := protocolRoutingOpenAIAccount(1)
	legal := protocolRoutingOpenAIAccount(2, "chat_completions")

	if ProtocolRouteLegal(ctx, missing, "gpt-5.4") {
		t.Fatal("account without supported_protocols passed the protocol hard gate")
	}
	if !ProtocolRouteLegal(ctx, legal, "gpt-5.4") {
		t.Fatal("account with messages -> chat_completions route failed the protocol hard gate")
	}
}

func TestProtocolRoutingDoesNotGovernOutOfScopeGeminiTransport(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages),
	)
	account := &Account{ID: 9, Platform: PlatformGemini, Type: AccountTypeOAuth}
	if !ProtocolRouteLegal(ctx, account, "gemini-3-flash") {
		t.Fatal("out-of-scope Gemini generateContent account was blocked")
	}
}

func TestOpenAIEligibilityUsesProtocolHardGateWithoutChangingOtherChecks(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions),
	)
	missing := protocolRoutingOpenAIAccount(1)
	legal := protocolRoutingOpenAIAccount(2, "chat_completions")

	if isOpenAICompatibleAccountEligibleForRequest(ctx, missing, PlatformOpenAI, "gpt-5.4", false, OpenAIEndpointCapabilityChatCompletions) {
		t.Fatal("OpenAI eligibility admitted account without legal protocol route")
	}
	if !isOpenAICompatibleAccountEligibleForRequest(ctx, legal, PlatformOpenAI, "gpt-5.4", false, OpenAIEndpointCapabilityChatCompletions) {
		t.Fatal("OpenAI eligibility rejected otherwise-valid legal account")
	}
}

func TestAdvancedSchedulerCompatibilityUsesProtocolHardGate(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions),
	)
	scheduler := &defaultOpenAIAccountScheduler{}
	req := OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.4"}

	compatible, _ := scheduler.isAccountRequestCompatibleReason(ctx, protocolRoutingOpenAIAccount(1), req)
	if compatible {
		t.Fatal("advanced scheduler admitted governed account without a legal protocol route")
	}
	compatible, reason := scheduler.isAccountRequestCompatibleReason(ctx, protocolRoutingOpenAIAccount(2, "chat_completions"), req)
	if !compatible {
		t.Fatalf("advanced scheduler rejected legal protocol route: %s", reason)
	}
}

func TestSelectionAttachesPlanForHydratedAccount(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolResponses),
	)
	account := protocolRoutingOpenAIAccount(7, "chat_completions")
	svc := &OpenAIGatewayService{}
	if !ProtocolRouteLegal(ctx, account, "gpt-5.4") {
		t.Fatal("scheduler rejected legal protocol route")
	}

	selection, err := svc.newSelectionResult(ctx, account, false, nil, nil)
	if err != nil {
		t.Fatalf("newSelectionResult: %v", err)
	}
	plan, ok := ProtocolPlanFromSelection(selection)
	if !ok {
		t.Fatal("selection has no protocol plan")
	}
	if plan.AccountID() != 7 || plan.TargetProtocol() != protocolrouter.ProtocolChatCompletions {
		t.Fatalf("selection plan account/target = %d/%q", plan.AccountID(), plan.TargetProtocol())
	}
}

func TestSelectionRejectsGovernedAccountWithoutSchedulerCreatedPlan(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolResponses),
	)
	account := protocolRoutingOpenAIAccount(8, "chat_completions")

	_, err := (&OpenAIGatewayService{}).newSelectionResult(ctx, account, false, nil, nil)
	if !errors.Is(err, ErrProtocolRouteUnavailable) {
		t.Fatalf("newSelectionResult error = %v, want ErrProtocolRouteUnavailable", err)
	}
}

func TestSelectionRejectsAccountChangedAfterSchedulerPlan(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolResponses),
	)
	account := protocolRoutingOpenAIAccount(9, "chat_completions")
	if !ProtocolRouteLegal(ctx, account, "gpt-5.4") {
		t.Fatal("scheduler rejected legal protocol route")
	}
	account.Credentials["base_url"] = "https://changed.example.test/v1"

	_, err := (&OpenAIGatewayService{}).newSelectionResult(ctx, account, false, nil, nil)
	if !errors.Is(err, protocolrouter.ErrStalePlan) {
		t.Fatalf("newSelectionResult error = %v, want ErrStalePlan", err)
	}
}

func TestSelectionFailsClosedAndReleasesAcquiredSlotWhenHydratedPlanIsIllegal(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages),
	)
	released := false
	svc := &OpenAIGatewayService{}

	_, err := svc.newSelectionResult(ctx, protocolRoutingOpenAIAccount(7), true, func() { released = true }, nil)
	if !errors.Is(err, ErrProtocolRouteUnavailable) {
		t.Fatalf("newSelectionResult error = %v, want ErrProtocolRouteUnavailable", err)
	}
	if !released {
		t.Fatal("acquired account slot was not released after protocol re-plan failure")
	}
}
