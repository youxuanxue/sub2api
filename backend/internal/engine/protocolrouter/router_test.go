package protocolrouter

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recordingAdapter struct {
	calls     int
	execution Execution
	result    Result
	err       error
}

func (a *recordingAdapter) Execute(_ context.Context, execution Execution) (Result, error) {
	a.calls++
	a.execution = execution
	return a.result, a.err
}

func testRequest(t *testing.T, protocol Protocol, profile RequestProfile) CanonicalRequest {
	t.Helper()
	if profile.ContentKinds == 0 {
		profile.ContentKinds = ContentText
	}
	req, err := NewCanonicalRequest(CanonicalRequestInput{
		InboundProtocol: protocol,
		RequestedModel:  "client-model",
		ResponsesPath:   ResponsesPathRoot,
		Profile:         profile,
		Body:            []byte(`{"model":"client-model","stream":false}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	return req
}

func testAccount(t *testing.T, supported ...Protocol) AccountSnapshot {
	t.Helper()
	allowed := make(map[Protocol]bool, len(supported))
	exactEndpoints := make(map[Protocol]string)
	geminiProfile := GeminiEndpointNone
	for _, protocol := range supported {
		allowed[protocol] = true
		if protocol == ProtocolGeminiGenerateContent {
			geminiProfile = GeminiEndpointAntigravityCloudCode
			exactEndpoints[protocol] = "https://cloudcode-pa.googleapis.com/v1internal:generateContent"
		}
	}
	account, err := NewAccountSnapshot(AccountSnapshotInput{
		AccountID:          42,
		Revision:           "rev-1",
		SupportedProtocols: supported,
		ResolvedModel:      "upstream-model",
		CustomBaseURL:      "https://relay.example.test/v1",
		ExactEndpoints:     exactEndpoints,
		GeminiProfile:      geminiProfile,
		ModelAllowed:       allowed,
		Transports:         []TransportID{TransportHTTP},
	})
	if err != nil {
		t.Fatalf("NewAccountSnapshot: %v", err)
	}
	return account
}

func allTestAdapters() AdapterCatalog {
	return AdapterCatalog{
		AdapterMessagesIdentity:    &recordingAdapter{},
		AdapterMessagesToResponses: &recordingAdapter{},
		AdapterMessagesToChat:      &recordingAdapter{},
		AdapterChatIdentity:        &recordingAdapter{},
		AdapterChatToResponses:     &recordingAdapter{},
		AdapterChatToMessages:      &recordingAdapter{},
		AdapterResponsesIdentity:   &recordingAdapter{},
		AdapterResponsesToChat:     &recordingAdapter{},
		AdapterResponsesToMessages: &recordingAdapter{},
		AdapterMessagesToGemini:    &recordingAdapter{},
		AdapterChatToGemini:        &recordingAdapter{},
		AdapterResponsesToGemini:   &recordingAdapter{},
		AdapterGeminiIdentity:      &recordingAdapter{},
	}
}

func TestPlanGeminiIdentityRequiresTypedProfileAndExactEndpoint(t *testing.T) {
	request := testRequest(t, ProtocolGeminiGenerateContent, RequestProfile{ContentKinds: ContentText})
	input := AccountSnapshotInput{
		AccountID:          42,
		Revision:           "rev-1",
		SupportedProtocols: []Protocol{ProtocolGeminiGenerateContent},
		ResolvedModel:      "gemini-2.5-pro",
		ModelAllowed:       map[Protocol]bool{ProtocolGeminiGenerateContent: true},
		Transports:         []TransportID{TransportHTTP},
	}

	for _, tc := range []struct {
		name     string
		profile  GeminiEndpointProfile
		endpoint string
	}{
		{name: "missing profile", endpoint: "https://cloudcode-pa.googleapis.com/v1internal:generateContent"},
		{name: "missing exact endpoint", profile: GeminiEndpointAntigravityCloudCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := input
			candidate.GeminiProfile = tc.profile
			candidate.ExactEndpoints = map[Protocol]string{ProtocolGeminiGenerateContent: tc.endpoint}
			account, err := NewAccountSnapshot(candidate)
			if err != nil {
				t.Fatalf("NewAccountSnapshot: %v", err)
			}
			if _, err := New(allTestAdapters()).Plan(request, account); !errors.Is(err, ErrNoLegalRoute) {
				t.Fatalf("Plan error = %v, want ErrNoLegalRoute", err)
			}
		})
	}
}

func TestPlanGeminiProfilesUseExactEndpoint(t *testing.T) {
	for _, profile := range []GeminiEndpointProfile{
		GeminiEndpointAntigravityCloudCode,
		GeminiEndpointVertexServiceAccount,
	} {
		t.Run(string(profile), func(t *testing.T) {
			const endpoint = "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-pro:generateContent"
			account, err := NewAccountSnapshot(AccountSnapshotInput{
				AccountID:          42,
				Revision:           "rev-1",
				SupportedProtocols: []Protocol{ProtocolGeminiGenerateContent},
				ResolvedModel:      "gemini-2.5-pro",
				ExactEndpoints:     map[Protocol]string{ProtocolGeminiGenerateContent: endpoint},
				GeminiProfile:      profile,
				ModelAllowed:       map[Protocol]bool{ProtocolGeminiGenerateContent: true},
				Transports:         []TransportID{TransportHTTP},
			})
			if err != nil {
				t.Fatalf("NewAccountSnapshot: %v", err)
			}
			plan, err := New(allTestAdapters()).Plan(testRequest(t, ProtocolGeminiGenerateContent, RequestProfile{}), account)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.Endpoint() != endpoint || plan.TargetProtocol() != ProtocolGeminiGenerateContent {
				t.Fatalf("plan endpoint/target = %q/%q", plan.Endpoint(), plan.TargetProtocol())
			}
		})
	}
}

func TestPlanGeminiConversionRejectsUnsupportedSemantics(t *testing.T) {
	account := testAccount(t, ProtocolGeminiGenerateContent)
	for _, profile := range []RequestProfile{
		{Tools: true, ContentKinds: ContentText},
		{Reasoning: ReasoningEffort, ContentKinds: ContentText},
		{PromptCache: PromptCacheKey, ContentKinds: ContentText},
		{Continuation: ContinuationPreviousResponse, ContentKinds: ContentText},
		{ContentKinds: ContentText | ContentImage},
	} {
		if _, err := New(allTestAdapters()).Plan(testRequest(t, ProtocolMessages, profile), account); !errors.Is(err, ErrNoLegalRoute) {
			t.Fatalf("Plan profile=%+v error = %v, want ErrNoLegalRoute", profile, err)
		}
	}
}

func TestPlanPrefersIdentityBeforeConversion(t *testing.T) {
	router := New(allTestAdapters())
	req := testRequest(t, ProtocolMessages, RequestProfile{})
	account := testAccount(t, ProtocolResponses, ProtocolMessages)

	plan, err := router.Plan(req, account)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.TargetProtocol() != ProtocolMessages {
		t.Fatalf("target = %q, want %q", plan.TargetProtocol(), ProtocolMessages)
	}
	if plan.RouteKind() != RouteIdentity {
		t.Fatalf("route kind = %q, want %q", plan.RouteKind(), RouteIdentity)
	}
}

func TestPlanUsesFixedFirstLegalConversion(t *testing.T) {
	router := New(allTestAdapters())
	req := testRequest(t, ProtocolMessages, RequestProfile{})
	account := testAccount(t, ProtocolChatCompletions, ProtocolResponses)

	plan, err := router.Plan(req, account)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.TargetProtocol() != ProtocolResponses {
		t.Fatalf("target = %q, want %q", plan.TargetProtocol(), ProtocolResponses)
	}
}

func TestPlanFailsClosedWhenCapabilityIsMissing(t *testing.T) {
	router := New(allTestAdapters())
	req := testRequest(t, ProtocolMessages, RequestProfile{})
	account := testAccount(t)

	_, err := router.Plan(req, account)
	if !errors.Is(err, ErrNoLegalRoute) {
		t.Fatalf("Plan error = %v, want ErrNoLegalRoute", err)
	}
}

func TestPlanFailsClosedWhenCustomEndpointIsMissing(t *testing.T) {
	account, err := NewAccountSnapshot(AccountSnapshotInput{
		AccountID:          42,
		Revision:           "rev-1",
		SupportedProtocols: []Protocol{ProtocolMessages},
		ResolvedModel:      "upstream-model",
		ModelAllowed:       map[Protocol]bool{ProtocolMessages: true},
		Transports:         []TransportID{TransportHTTP},
	})
	if err != nil {
		t.Fatalf("NewAccountSnapshot: %v", err)
	}

	_, err = New(allTestAdapters()).Plan(testRequest(t, ProtocolMessages, RequestProfile{}), account)
	if !errors.Is(err, ErrNoLegalRoute) {
		t.Fatalf("Plan error = %v, want ErrNoLegalRoute", err)
	}
}

func TestPlanUsesFixedOpenAICodexEndpointOnlyForResponses(t *testing.T) {
	account, err := NewAccountSnapshot(AccountSnapshotInput{
		AccountID:          42,
		Revision:           "rev-1",
		SupportedProtocols: []Protocol{ProtocolResponses, ProtocolChatCompletions},
		ResolvedModel:      "gpt-5.4",
		OfficialProfile:    OfficialEndpointOpenAICodex,
		ModelAllowed: map[Protocol]bool{
			ProtocolResponses:       true,
			ProtocolChatCompletions: true,
		},
		Transports: []TransportID{TransportHTTP},
	})
	if err != nil {
		t.Fatalf("NewAccountSnapshot: %v", err)
	}

	plan, err := New(allTestAdapters()).Plan(testRequest(t, ProtocolResponses, RequestProfile{}), account)
	if err != nil {
		t.Fatalf("Plan responses: %v", err)
	}
	if got, want := plan.Endpoint(), "https://chatgpt.com/backend-api/codex/responses"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}

	chatPlan, err := New(allTestAdapters()).Plan(testRequest(t, ProtocolChatCompletions, RequestProfile{}), account)
	if err != nil {
		t.Fatalf("Plan chat conversion: %v", err)
	}
	if chatPlan.TargetProtocol() != ProtocolResponses || chatPlan.Endpoint() != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("chat plan target/endpoint = %q/%q, want responses on fixed Codex endpoint", chatPlan.TargetProtocol(), chatPlan.Endpoint())
	}

	inputTokensRequest, err := NewCanonicalRequest(CanonicalRequestInput{
		InboundProtocol: ProtocolResponses,
		RequestedModel:  "gpt-5.4",
		ResponsesPath:   ResponsesPathInputTokens,
		Profile:         RequestProfile{ContentKinds: ContentText},
		Body:            []byte(`{"model":"gpt-5.4","input":"hello"}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest input_tokens: %v", err)
	}
	inputTokensPlan, err := New(allTestAdapters()).Plan(inputTokensRequest, account)
	if err != nil {
		t.Fatalf("Plan input_tokens: %v", err)
	}
	if inputTokensPlan.ResponsesPath() != ResponsesPathInputTokens ||
		inputTokensPlan.Endpoint() != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("input_tokens plan path/endpoint = %q/%q, want input_tokens anchored to Codex responses", inputTokensPlan.ResponsesPath(), inputTokensPlan.Endpoint())
	}
}

func TestPlanSkipsRouteWithoutAdapter(t *testing.T) {
	router := New(AdapterCatalog{AdapterMessagesToChat: &recordingAdapter{}})
	req := testRequest(t, ProtocolMessages, RequestProfile{})
	account := testAccount(t, ProtocolResponses, ProtocolChatCompletions)

	plan, err := router.Plan(req, account)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.TargetProtocol() != ProtocolChatCompletions {
		t.Fatalf("target = %q, want chat fallback after missing responses adapter", plan.TargetProtocol())
	}
}

func TestPlanRejectsConversionThatCannotPreserveContinuation(t *testing.T) {
	router := New(allTestAdapters())
	req := testRequest(t, ProtocolResponses, RequestProfile{Continuation: ContinuationPreviousResponse})
	account := testAccount(t, ProtocolChatCompletions, ProtocolMessages)

	_, err := router.Plan(req, account)
	if !errors.Is(err, ErrNoLegalRoute) {
		t.Fatalf("Plan error = %v, want ErrNoLegalRoute", err)
	}
}

func TestPlanRejectsConversionThatCannotPreserveToolsOrUnknownContent(t *testing.T) {
	router := New(allTestAdapters())
	account := testAccount(t, ProtocolChatCompletions)
	for _, profile := range []RequestProfile{
		{Tools: true, ContentKinds: ContentText},
		{ToolChoice: ToolChoiceRequired, ContentKinds: ContentText},
		{ContentKinds: ContentText | ContentUnknown},
	} {
		_, err := router.Plan(testRequest(t, ProtocolMessages, profile), account)
		if !errors.Is(err, ErrNoLegalRoute) {
			t.Fatalf("Plan profile=%+v error = %v, want ErrNoLegalRoute", profile, err)
		}
	}
}

func TestRouteRegistryRejectsIncompleteExecutionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]routeEntry)
	}{
		{name: "model policy", mutate: func(entries []routeEntry) { entries[0].model = nil }},
		{name: "feature policy", mutate: func(entries []routeEntry) { entries[0].preserves = nil }},
		{name: "endpoint resolver", mutate: func(entries []routeEntry) { entries[0].endpoint = nil }},
		{name: "adapter", mutate: func(entries []routeEntry) { entries[0].adapterID = "" }},
		{name: "transport", mutate: func(entries []routeEntry) { entries[0].transport = "" }},
		{name: "responses paths", mutate: func(entries []routeEntry) { entries[1].responsesPaths = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := append([]routeEntry(nil), routeRegistry...)
			tt.mutate(entries)
			if err := validateRouteRegistry(entries); err == nil {
				t.Fatalf("validateRouteRegistry accepted route without %s", tt.name)
			}
		})
	}
}

func TestResponsesIdentityDeclaresAllowedPathsAndConversionsStayOnRoot(t *testing.T) {
	router := New(allTestAdapters())
	for _, path := range []ResponsesPathKind{ResponsesPathRoot, ResponsesPathCompact, ResponsesPathInputTokens} {
		req, err := NewCanonicalRequest(CanonicalRequestInput{
			InboundProtocol: ProtocolResponses,
			RequestedModel:  "client-model",
			ResponsesPath:   path,
			Profile:         RequestProfile{ContentKinds: ContentText},
			Body:            []byte(`{"model":"client-model","input":"hello"}`),
		})
		if err != nil {
			t.Fatalf("NewCanonicalRequest(%s): %v", path, err)
		}
		plan, err := router.Plan(req, testAccount(t, ProtocolResponses))
		if err != nil {
			t.Fatalf("Plan(%s): %v", path, err)
		}
		if plan.ResponsesPath() != path {
			t.Fatalf("Plan(%s) path = %q", path, plan.ResponsesPath())
		}
		if path != ResponsesPathRoot {
			if _, err := router.Plan(req, testAccount(t, ProtocolChatCompletions, ProtocolMessages)); !errors.Is(err, ErrNoLegalRoute) {
				t.Fatalf("conversion Plan(%s) error = %v, want ErrNoLegalRoute", path, err)
			}
		}
	}
}

func TestCanonicalRequestCopiesBodyAndAccessorsReturnCopies(t *testing.T) {
	body := []byte(`{"model":"client-model"}`)
	req, err := NewCanonicalRequest(CanonicalRequestInput{
		InboundProtocol: ProtocolMessages,
		RequestedModel:  "client-model",
		Body:            body,
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	body[2] = 'X'
	first := req.Body()
	first[2] = 'Y'
	if got := string(req.Body()); got != `{"model":"client-model"}` {
		t.Fatalf("immutable body = %q", got)
	}
}

func TestExecuteRejectsDifferentRequestBeforeAdapter(t *testing.T) {
	adapter := &recordingAdapter{}
	router := New(AdapterCatalog{AdapterMessagesIdentity: adapter})
	req := testRequest(t, ProtocolMessages, RequestProfile{})
	plan, err := router.Plan(req, testAccount(t, ProtocolMessages))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	different, err := NewCanonicalRequest(CanonicalRequestInput{
		InboundProtocol: ProtocolMessages,
		RequestedModel:  "different-model",
		Body:            []byte(`{"model":"different-model"}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	ctx := WithExecutionAccountState(context.Background(), ExecutionAccountState{
		AccountID:         42,
		Revision:          "rev-1",
		CredentialPresent: true,
	})

	_, err = router.Execute(ctx, plan, different)
	if !errors.Is(err, ErrStalePlan) {
		t.Fatalf("Execute error = %v, want ErrStalePlan", err)
	}
	if adapter.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.calls)
	}
}

func TestExecuteRejectsStaleAccountOrMissingCredentialBeforeAdapter(t *testing.T) {
	tests := []struct {
		name  string
		state ExecutionAccountState
		want  error
	}{
		{name: "stale revision", state: ExecutionAccountState{AccountID: 42, Revision: "rev-2", CredentialPresent: true}, want: ErrStalePlan},
		{name: "missing credential", state: ExecutionAccountState{AccountID: 42, Revision: "rev-1"}, want: ErrMissingCredential},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &recordingAdapter{}
			router := New(AdapterCatalog{AdapterMessagesIdentity: adapter})
			req := testRequest(t, ProtocolMessages, RequestProfile{})
			plan, err := router.Plan(req, testAccount(t, ProtocolMessages))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}

			_, err = router.Execute(WithExecutionAccountState(context.Background(), tt.state), plan, req)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Execute error = %v, want %v", err, tt.want)
			}
			if adapter.calls != 0 {
				t.Fatalf("adapter calls = %d, want 0", adapter.calls)
			}
		})
	}
}

func TestExecuteInvokesExactlyThePlannedAdapter(t *testing.T) {
	identity := &recordingAdapter{result: Result{Value: "ok"}}
	conversion := &recordingAdapter{}
	router := New(AdapterCatalog{
		AdapterMessagesIdentity:    identity,
		AdapterMessagesToResponses: conversion,
	})
	req := testRequest(t, ProtocolMessages, RequestProfile{})
	plan, err := router.Plan(req, testAccount(t, ProtocolMessages, ProtocolResponses))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ctx := WithExecutionAccountState(context.Background(), ExecutionAccountState{
		AccountID:         42,
		Revision:          "rev-1",
		CredentialPresent: true,
	})

	result, err := router.Execute(ctx, plan, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !reflect.DeepEqual(result.Value, "ok") {
		t.Fatalf("result = %#v", result.Value)
	}
	if identity.calls != 1 || conversion.calls != 0 {
		t.Fatalf("adapter calls identity=%d conversion=%d", identity.calls, conversion.calls)
	}
	if identity.execution.Plan().AdapterID() != AdapterMessagesIdentity {
		t.Fatalf("executed adapter = %q", identity.execution.Plan().AdapterID())
	}
}

func TestRouteRegistryEntriesPlanAndExecuteTheirDeclaredAdapter(t *testing.T) {
	for _, route := range routeRegistry {
		route := route
		t.Run(string(route.adapterID), func(t *testing.T) {
			adapter := &recordingAdapter{result: Result{Value: route.adapterID}}
			router := New(AdapterCatalog{route.adapterID: adapter})
			request := testRequest(t, route.inbound, RequestProfile{})
			account := testAccount(t, route.target)

			plan, err := router.Plan(request, account)
			if err != nil {
				t.Fatalf("Plan(%s -> %s): %v", route.inbound, route.target, err)
			}
			if plan.TargetProtocol() != route.target || plan.AdapterID() != route.adapterID ||
				plan.Transport() != route.transport || plan.RouteKind() != route.kind {
				t.Fatalf(
					"plan = target %q adapter %q transport %q kind %q; want %q %q %q %q",
					plan.TargetProtocol(), plan.AdapterID(), plan.Transport(), plan.RouteKind(),
					route.target, route.adapterID, route.transport, route.kind,
				)
			}

			ctx := WithExecutionAccountState(context.Background(), ExecutionAccountState{
				AccountID:         account.AccountID(),
				Revision:          account.Revision(),
				CredentialPresent: true,
			})
			result, err := router.Execute(ctx, plan, request)
			if err != nil {
				t.Fatalf("Execute(%s): %v", route.adapterID, err)
			}
			if result.Value != route.adapterID || adapter.calls != 1 {
				t.Fatalf("result/calls = %#v/%d, want %q/1", result.Value, adapter.calls, route.adapterID)
			}
		})
	}
}
