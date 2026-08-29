package service

import (
	"context"
	"errors"
	"testing"
	"time"

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
	supported := make([]protocolrouter.Protocol, len(protocols))
	for i, protocol := range protocols {
		supported[i] = protocolrouter.Protocol(protocol)
	}
	account := &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
		Extra: map[string]any{},
	}
	attachTestProtocolCapability(account, supported...)
	return account
}

func attachTestProtocolCapability(account *Account, protocols ...protocolrouter.Protocol) {
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		panic("invalid governed protocol test account")
	}
	id := account.ID + 100000
	if id <= 0 {
		id = 100000
	}
	account.ProtocolEndpointCapabilityID = &id
	account.ProtocolEndpointCapability = &ProtocolEndpointCapability{
		ID:                 id,
		CapabilityKey:      identity.Key(),
		Identity:           identity,
		SupportedProtocols: append([]protocolrouter.Protocol(nil), protocols...),
		Revision:           1,
		ProbeEvidence:      ProtocolProbeEvidence{InitialProbeCompleted: true},
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
	missingAuthorization := protocolRoutingOpenAIAccount(3, "chat_completions")
	delete(missingAuthorization.Credentials, "api_key")

	if isOpenAICompatibleAccountEligibleForRequest(ctx, missing, PlatformOpenAI, "gpt-5.4", false, OpenAIEndpointCapabilityChatCompletions) {
		t.Fatal("OpenAI eligibility admitted account without legal protocol route")
	}
	if !isOpenAICompatibleAccountEligibleForRequest(ctx, legal, PlatformOpenAI, "gpt-5.4", false, OpenAIEndpointCapabilityChatCompletions) {
		t.Fatal("OpenAI eligibility rejected otherwise-valid legal account")
	}
	if isOpenAICompatibleAccountEligibleForRequest(ctx, missingAuthorization, PlatformOpenAI, "gpt-5.4", false, OpenAIEndpointCapabilityChatCompletions) {
		t.Fatal("OpenAI eligibility admitted a governed account without authorization")
	}
	if (&GatewayService{}).isAccountSchedulableForModelSelection(ctx, missingAuthorization, "gpt-5.4") {
		t.Fatal("gateway scheduling admitted a governed account without authorization")
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

func TestCanonicalPlanningOwnsGovernedModelRejectionReason(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions),
	)
	account := protocolRoutingOpenAIAccount(3, "chat_completions")
	account.Credentials["model_mapping"] = map[string]any{
		"another-model": "upstream-model",
	}

	eligible, reason := protocolRequestEligibilityReason(ctx, account, "gpt-5.4")
	if eligible || reason != "model_not_supported" {
		t.Fatalf("eligibility = %v/%q, want false/model_not_supported", eligible, reason)
	}
}

func TestOpenAICompatEligibilityReasonUsesCanonicalModelRejection(t *testing.T) {
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses,
		RequestedModel:  "client-alias",
		Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body:            []byte(`{"model":"client-alias","input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	ctx := WithProtocolRouting(context.Background(), protocolRoutingTestRouter(), request)
	account := &Account{
		ID:          31,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "secret",
			"model_mapping": map[string]any{"client-alias": "deepseek-v3"},
		},
		Extra: map[string]any{SupportedProtocolsExtraKey: []any{"responses"}},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	if !account.IsModelSupported("client-alias") {
		t.Fatal("test precondition: legacy account check must admit the explicit alias")
	}

	reason := openAICompatEligibilityReason(
		ctx,
		account,
		PlatformOpenAI,
		"client-alias",
		false,
		OpenAIEndpointCapabilityResponses,
	)
	if reason != openAICompatIneligibleModelUnsupported {
		t.Fatalf("eligibility reason = %q, want canonical %q", reason, openAICompatIneligibleModelUnsupported)
	}
}

func TestFailureDiagnosisDoesNotRenameNoRouteAsUnsupportedModel(t *testing.T) {
	ctx := WithProtocolRouting(
		context.Background(),
		protocolRoutingTestRouter(),
		protocolRoutingTestRequest(t, protocolrouter.ProtocolChatCompletions),
	)
	account := protocolRoutingOpenAIAccount(4)
	account.Credentials["model_mapping"] = map[string]any{
		"another-model": "upstream-model",
	}

	if (&OpenAIGatewayService{}).isOpenAICompatModelUnservableForRequest(
		ctx, nil, account, "gpt-5.4", false, false,
	) {
		t.Fatal("protocol no-route was misclassified as model unsupported")
	}
}

func TestProtocolPlanCacheComputesEachAccountRevisionOnce(t *testing.T) {
	cache := newProtocolPlanCache()
	key := protocolPlanCacheKey{accountID: 9, revision: "rev-1"}
	wantErr := errors.New("no route")
	calls := 0
	compute := func() (protocolrouter.Plan, error) {
		calls++
		return protocolrouter.Plan{}, wantErr
	}

	_, firstErr := cache.getOrPlan(key, compute)
	_, secondErr := cache.getOrPlan(key, compute)

	if !errors.Is(firstErr, wantErr) || !errors.Is(secondErr, wantErr) {
		t.Fatalf("cached errors = %v / %v, want %v", firstErr, secondErr, wantErr)
	}
	if calls != 1 {
		t.Fatalf("planner calls = %d, want 1 for one account revision", calls)
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
	account.Credentials["api_key"] = "rotated-secret"
	account.UpdatedAt = account.UpdatedAt.Add(time.Nanosecond)

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
	releaseCalls := 0
	svc := &OpenAIGatewayService{}

	_, err := svc.newAcquiredSelectionResult(ctx, protocolRoutingOpenAIAccount(7), func() { releaseCalls++ })
	if !errors.Is(err, ErrProtocolRouteUnavailable) {
		t.Fatalf("newAcquiredSelectionResult error = %v, want ErrProtocolRouteUnavailable", err)
	}
	if releaseCalls != 1 {
		t.Fatalf("expected acquired account slot release once after protocol re-plan failure, got %d", releaseCalls)
	}
}
