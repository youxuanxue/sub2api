package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

type supportedProtocolsUpdateRecorder struct {
	accountID int64
	updates   map[string]any
}

func (r *supportedProtocolsUpdateRecorder) UpdateExtra(_ context.Context, accountID int64, updates map[string]any) error {
	r.accountID = accountID
	r.updates = updates
	return nil
}

func TestNormalizeSupportedProtocolsValidatesDeduplicatesAndOrders(t *testing.T) {
	input := []protocolrouter.Protocol{
		protocolrouter.ProtocolResponses,
		protocolrouter.ProtocolMessages,
		protocolrouter.ProtocolResponses,
		protocolrouter.ProtocolChatCompletions,
	}
	original := append([]protocolrouter.Protocol(nil), input...)

	got, err := NormalizeSupportedProtocols(input)
	if err != nil {
		t.Fatalf("NormalizeSupportedProtocols: %v", err)
	}
	want := []protocolrouter.Protocol{
		protocolrouter.ProtocolMessages,
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ProtocolResponses,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("input mutated: got %v, want %v", input, original)
	}

	if _, err := NormalizeSupportedProtocols([]protocolrouter.Protocol{"unknown"}); err == nil {
		t.Fatal("expected invalid protocol error")
	}
}

func TestAccountSupportedProtocolsReadsOnlyLinkedCapability(t *testing.T) {
	account := &Account{Extra: map[string]any{
		SupportedProtocolsExtraKey:         []any{"chat_completions"},
		"openai_responses_supported":       true,
		"openai_native_messages_supported": true,
	}}
	account.ProtocolEndpointCapability = &ProtocolEndpointCapability{SupportedProtocols: []protocolrouter.Protocol{
		protocolrouter.ProtocolResponses, protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses,
	}}

	got := account.SupportedProtocols()
	want := []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedProtocols() = %v, want %v", got, want)
	}

	legacyOnly := &Account{Extra: map[string]any{
		"openai_responses_supported":       true,
		"openai_native_messages_supported": true,
	}}
	if got := legacyOnly.SupportedProtocols(); len(got) != 0 {
		t.Fatalf("legacy fields leaked into canonical protocols: %v", got)
	}
}

func TestReplaceSupportedProtocolsWritesCompleteDeterministicSet(t *testing.T) {
	recorder := &supportedProtocolsUpdateRecorder{}
	err := ReplaceSupportedProtocols(context.Background(), recorder, 91, []protocolrouter.Protocol{
		protocolrouter.ProtocolResponses,
		protocolrouter.ProtocolMessages,
		protocolrouter.ProtocolMessages,
	})
	if err != nil {
		t.Fatalf("ReplaceSupportedProtocols: %v", err)
	}
	if recorder.accountID != 91 {
		t.Fatalf("account id = %d, want 91", recorder.accountID)
	}
	want := []string{"messages", "responses"}
	if got := recorder.updates[SupportedProtocolsExtraKey]; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted protocols = %#v, want %#v", got, want)
	}

	err = ReplaceSupportedProtocols(context.Background(), recorder, 91, nil)
	if err != nil {
		t.Fatalf("ReplaceSupportedProtocols empty: %v", err)
	}
	if got := recorder.updates[SupportedProtocolsExtraKey]; !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("empty persisted protocols = %#v, want empty string slice", got)
	}
}

func TestBuildSupportedProtocolsUpdateOwnsCanonicalEncoding(t *testing.T) {
	update, err := BuildSupportedProtocolsUpdate([]protocolrouter.Protocol{
		protocolrouter.ProtocolResponses,
		protocolrouter.ProtocolMessages,
		protocolrouter.ProtocolResponses,
	})
	if err != nil {
		t.Fatalf("BuildSupportedProtocolsUpdate: %v", err)
	}
	want := []string{"messages", "responses"}
	if got := update[SupportedProtocolsExtraKey]; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted protocols = %#v, want %#v", got, want)
	}
	if _, err := BuildSupportedProtocolsUpdate([]protocolrouter.Protocol{"unknown"}); err == nil {
		t.Fatal("expected invalid protocol error")
	}
}

func TestProtocolResolvedModelAllowedForTarget_DualStackMessagesRequiresClaude(t *testing.T) {
	account := protocolRoutingOpenAIAccount(92, "messages", "chat_completions")
	if !protocolResolvedModelAllowedForTarget(account, "", protocolrouter.ProtocolMessages, "claude-sonnet-4-6", "claude-sonnet-4-6", nil) {
		t.Fatal("claude model must remain legal for native messages")
	}
	if protocolResolvedModelAllowedForTarget(account, "", protocolrouter.ProtocolMessages, "gpt-5.4", "gpt-5.4", nil) {
		t.Fatal("gpt-5.4 must not be legal for native messages on a dual-stack OpenAI relay")
	}
	if !protocolResolvedModelAllowedForTarget(account, "", protocolrouter.ProtocolChatCompletions, "gpt-5.4", "gpt-5.4", nil) {
		t.Fatal("gpt-5.4 must remain legal for chat_completions")
	}

	tokensea := protocolRoutingRelinkedOpenAIAccount(92, "https://agent.tokensea.ai/v1", "messages", "chat_completions", "responses")
	if protocolResolvedModelAllowedForTarget(tokensea, "", protocolrouter.ProtocolResponses, "gpt-5.4", "gpt-5.4", nil) {
		t.Fatal("tokensea must not plan native responses")
	}

	cloudwise := protocolRoutingRelinkedOpenAIAccount(95, "https://api.cloudwise.ai/api", "messages", "chat_completions", "responses")
	if protocolResolvedModelAllowedForTarget(cloudwise, "", protocolrouter.ProtocolMessages, "MiniMax-M3", "MiniMax-M3", nil) {
		t.Fatal("cloudwise MiniMax must not plan native messages")
	}
	if protocolResolvedModelAllowedForTarget(cloudwise, "", protocolrouter.ProtocolResponses, "MiniMax-M3", "MiniMax-M3", nil) {
		t.Fatal("cloudwise must not plan native responses")
	}
	if !protocolResolvedModelAllowedForTarget(cloudwise, "", protocolrouter.ProtocolChatCompletions, "MiniMax-M3", "MiniMax-M3", nil) {
		t.Fatal("cloudwise MiniMax must remain legal for chat_completions")
	}
}

func TestProtocolAccountSnapshotResolvesModelFromAccountFacts(t *testing.T) {
	updatedAt := time.Date(2026, 8, 24, 10, 30, 0, 0, time.UTC)
	account := &Account{
		ID:        42,
		Platform:  PlatformOpenAI,
		Type:      AccountTypeAPIKey,
		UpdatedAt: updatedAt,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      "https://relay.example.test/v1",
			"model_mapping": map[string]any{"client-model": "upstream-model"},
		},
		Extra: map[string]any{},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses)

	snapshot, err := ProtocolAccountSnapshot(account, "client-model")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot: %v", err)
	}
	if snapshot.AccountID() != 42 || snapshot.ResolvedModel() != "upstream-model" {
		t.Fatalf("snapshot account/model = %d/%q", snapshot.AccountID(), snapshot.ResolvedModel())
	}
	if snapshot.CapabilityKey() == "" {
		t.Fatal("snapshot capability key is empty")
	}

	noisy := *account
	noisy.UpdatedAt = updatedAt.Add(time.Nanosecond)
	noisy.Credentials = map[string]any{
		"api_key":       "different-secret",
		"base_url":      "https://relay.example.test/v1",
		"model_mapping": map[string]any{"client-model": "upstream-model"},
	}
	noisySnapshot, err := ProtocolAccountSnapshot(&noisy, "client-model")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot noisy: %v", err)
	}
	if noisySnapshot.CapabilityKey() != snapshot.CapabilityKey() || noisySnapshot.ResolvedModel() != snapshot.ResolvedModel() {
		t.Fatal("updated_at or secret rotation changed capability key or resolved model")
	}

	changed := *account
	changed.Credentials = map[string]any{
		"api_key":       "secret",
		"base_url":      "https://relay.example.test/v1",
		"model_mapping": map[string]any{"client-model": "other-upstream"},
	}
	changedSnapshot, err := ProtocolAccountSnapshot(&changed, "client-model")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot changed: %v", err)
	}
	if changedSnapshot.ResolvedModel() != "other-upstream" {
		t.Fatalf("mapping change resolved model = %q, want other-upstream", changedSnapshot.ResolvedModel())
	}
}

func TestProtocolAccountSnapshotRejectsUnverifiedHistoricalCapability(t *testing.T) {
	account := &Account{
		ID:       43,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
		Extra: map[string]any{},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	account.ProtocolEndpointCapability.ProbeEvidence.InitialProbeCompleted = false
	account.ProtocolEndpointCapability.ProbeEvidence.OfficialSeed = false

	if _, err := ProtocolAccountSnapshot(account, "gpt-5.4"); err == nil {
		t.Fatal("unverified historical capability was admitted to runtime routing")
	}
}

func TestProtocolAccountSnapshotAdmitsPartialPositiveProbeWithoutInitialCompletion(t *testing.T) {
	account := &Account{
		ID:       60,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      "https://relay.example.test/v1",
			"model_mapping": map[string]any{"qwen3.5-plus": "qwen3.5-plus"},
		},
		Extra: map[string]any{},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions)
	account.ProtocolEndpointCapability.ProbeEvidence.InitialProbeCompleted = false
	account.ProtocolEndpointCapability.ProbeEvidence.OfficialSeed = false
	account.ProtocolEndpointCapability.ProbeEvidence.Verdicts = map[string]any{
		string(protocolrouter.ProtocolChatCompletions): string(ProtocolProbePositive),
	}

	snapshot, err := ProtocolAccountSnapshot(account, "qwen3.5-plus")
	if err != nil {
		t.Fatalf("partial positive capability was excluded: %v", err)
	}
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolChatCompletions,
		RequestedModel:  "qwen3.5-plus",
		Body:            []byte(`{"model":"qwen3.5-plus","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	if _, err := NewProtocolRouter().Plan(request, snapshot); err != nil {
		t.Fatalf("partial positive chat_completions route failed: %v", err)
	}
}

func TestProtocolAccountSnapshotUsesOfficialProfileForOpenAIOAuth(t *testing.T) {
	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "oauth-secret",
		},
		Extra: map[string]any{},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	snapshot, err := ProtocolAccountSnapshot(account, "gpt-5.4")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot: %v", err)
	}
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses,
		RequestedModel:  "gpt-5.4",
		Body:            []byte(`{"model":"gpt-5.4","input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, want := plan.Endpoint(), "https://chatgpt.com/backend-api/codex/responses"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestProtocolAccountSnapshotUsesExplicitMessagesEndpointForCustomAnthropicOAuth(t *testing.T) {
	account := &Account{
		ID:       79,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "oauth-secret",
		},
		Extra: map[string]any{
			"custom_base_url_enabled": true,
			"custom_base_url":         "https://relay.example.test/v1",
		},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolMessages)
	snapshot, err := ProtocolAccountSnapshot(account, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot: %v", err)
	}
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolMessages,
		RequestedModel:  "claude-sonnet-4-6",
		Profile: protocolrouter.RequestProfile{
			ContentKinds: protocolrouter.ContentText,
		},
		Body: []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, want := plan.Endpoint(), "https://relay.example.test/v1/messages"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestProtocolAccountSnapshotUsesCanonicalAgentPlanEndpoints(t *testing.T) {
	account := &Account{
		ID:          88,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      newapiintegration.VolcEngineAgentPlanBaseURL,
			"model_mapping": map[string]any{"ark-code-latest": "ark-code-latest"},
		},
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []any{"chat_completions", "responses"},
		},
	}

	chatEndpoint, err := protocolExactEndpoint(account, protocolrouter.ProtocolChatCompletions, "ark-code-latest")
	if err != nil {
		t.Fatalf("protocolExactEndpoint chat: %v", err)
	}
	if got, want := chatEndpoint, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions"; got != want {
		t.Fatalf("chat endpoint = %q, want %q", got, want)
	}
	responsesEndpoint, err := protocolExactEndpoint(account, protocolrouter.ProtocolResponses, "ark-code-latest")
	if err != nil {
		t.Fatalf("protocolExactEndpoint responses: %v", err)
	}
	if got, want := responsesEndpoint, newapiintegration.VolcEngineAgentPlanBaseURL+"/responses"; got != want {
		t.Fatalf("responses endpoint = %q, want %q", got, want)
	}
}

func TestProtocolProbeCandidates_VolcAgentPlanChatOnly(t *testing.T) {
	account := &Account{
		ID:          125,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": newapiintegration.VolcEngineAgentPlanBaseURL,
		},
	}
	got := ProtocolProbeCandidates(account)
	want := []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probe candidates = %v, want %v", got, want)
	}
}

func TestVolcAgentPlanRoutingSupportedProtocols_ChatOnly(t *testing.T) {
	got := volcAgentPlanRoutingSupportedProtocols([]protocolrouter.Protocol{
		protocolrouter.ProtocolResponses,
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ProtocolMessages,
	})
	want := []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitize = %v, want %v", got, want)
	}
	if got := volcAgentPlanRoutingSupportedProtocols([]protocolrouter.Protocol{protocolrouter.ProtocolResponses}); got != nil {
		t.Fatalf("responses-only sanitize = %v, want nil", got)
	}

	account := &Account{
		ID:          125,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      newapiintegration.VolcEngineAgentPlanBaseURL,
			"model_mapping": map[string]any{"kimi-k3": "kimi-k3"},
		},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses)
	if got := routingSupportedProtocols(account); !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan-facing protocols = %v, want %v", got, want)
	}
	if protocolResolvedModelAllowedForTarget(account, "", protocolrouter.ProtocolResponses, "kimi-k3", "kimi-k3", nil) {
		t.Fatal("Agent Plan must not allow native Responses targets")
	}
	if !protocolResolvedModelAllowedForTarget(account, "", protocolrouter.ProtocolChatCompletions, "kimi-k3", "kimi-k3", nil) {
		t.Fatal("Agent Plan must allow Chat Completions")
	}
}

func TestProtocolAccountSnapshotUsesCanonicalQianfanTokenPlanEndpoints(t *testing.T) {
	account := &Account{
		ID:          130,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      newapiintegration.QianfanTokenPlanBaseURL,
			"model_mapping": map[string]any{"deepseek-v4-flash": "deepseek-v4-flash"},
		},
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []any{"chat_completions", "messages"},
		},
	}

	chatEndpoint, err := protocolExactEndpoint(account, protocolrouter.ProtocolChatCompletions, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("protocolExactEndpoint chat: %v", err)
	}
	if got, want := chatEndpoint, newapiintegration.QianfanTokenPlanBaseURL+"/chat/completions"; got != want {
		t.Fatalf("chat endpoint = %q, want %q", got, want)
	}
	messagesEndpoint, err := protocolExactEndpoint(account, protocolrouter.ProtocolMessages, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("protocolExactEndpoint messages: %v", err)
	}
	if got, want := messagesEndpoint, newapiintegration.QianfanTokenPlanAnthropicBaseURL+"/v1/messages"; got != want {
		t.Fatalf("messages endpoint = %q, want %q", got, want)
	}
	responsesEndpoint, err := protocolExactEndpoint(account, protocolrouter.ProtocolResponses, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("protocolExactEndpoint responses: %v", err)
	}
	if responsesEndpoint != "" {
		t.Fatalf("responses endpoint = %q, want empty (Token Plan has no responses surface)", responsesEndpoint)
	}
}

func TestProtocolExactEndpointsOmitsUnsupportedNewAPIProtocol(t *testing.T) {
	account := qianfanChatTestAccount(90)
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions)
	endpoints, err := protocolExactEndpoints(account, "ernie-5.0", protocolrouter.GeminiEndpointNone, false)
	if err != nil {
		t.Fatalf("protocolExactEndpoints: %v", err)
	}
	if got := endpoints[protocolrouter.ProtocolChatCompletions]; got != newapiintegration.QianfanBaseURL+"/v2/chat/completions" {
		t.Fatalf("chat exact endpoint = %q", got)
	}
	if _, ok := endpoints[protocolrouter.ProtocolResponses]; ok {
		t.Fatalf("unsupported Responses exact endpoint was attached: %#v", endpoints)
	}
}

func TestProtocolExactEndpointsOmitsUnresolvableClaimedResponses(t *testing.T) {
	// #1893 fail-closed when capability falsely claimed Responses. Keep Chat
	// and omit the unresolvable protocol instead of poisoning the snapshot.
	account := qianfanChatTestAccount(90)
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses)
	endpoints, err := protocolExactEndpoints(account, "ernie-5.0", protocolrouter.GeminiEndpointNone, false)
	if err != nil {
		t.Fatalf("claimed Responses must not fail Chat exact endpoints: %v", err)
	}
	if got := endpoints[protocolrouter.ProtocolChatCompletions]; got != newapiintegration.QianfanBaseURL+"/v2/chat/completions" {
		t.Fatalf("chat exact endpoint = %q", got)
	}
	if _, ok := endpoints[protocolrouter.ProtocolResponses]; ok {
		t.Fatalf("unresolvable claimed Responses was attached: %#v", endpoints)
	}
}

func TestProtocolExactEndpointsFailsWhenEveryDeclaredProtocolIsUnresolvable(t *testing.T) {
	account := qianfanChatTestAccount(90)
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	if _, err := protocolExactEndpoints(account, "ernie-5.0", protocolrouter.GeminiEndpointNone, false); err == nil {
		t.Fatal("expected Responses-only Qianfan exact endpoints to fail closed")
	}
}

func TestProtocolAccountSnapshotPlansQianfanChatWithoutResponses(t *testing.T) {
	account := qianfanChatTestAccount(90)
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions)
	account.ProtocolEndpointCapability.ProbeEvidence.InitialProbeCompleted = false
	account.ProtocolEndpointCapability.ProbeEvidence.OfficialSeed = false
	account.ProtocolEndpointCapability.ProbeEvidence.Verdicts = map[string]any{
		string(protocolrouter.ProtocolChatCompletions): string(ProtocolProbePositive),
		string(protocolrouter.ProtocolMessages):        string(ProtocolProbeEndpointNegative),
	}

	snapshot, err := ProtocolAccountSnapshot(account, "ernie-5.0")
	if err != nil {
		t.Fatalf("chat-only Qianfan snapshot failed: %v", err)
	}
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolChatCompletions,
		RequestedModel:  "ernie-5.0",
		Profile: protocolrouter.RequestProfile{
			ContentKinds: protocolrouter.ContentText,
		},
		Body: []byte(`{"model":"ernie-5.0","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, want := plan.TargetProtocol(), protocolrouter.ProtocolChatCompletions; got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
	if got, want := plan.Endpoint(), newapiintegration.QianfanBaseURL+"/v2/chat/completions"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}

	responsesReq, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses,
		RequestedModel:  "ernie-5.0",
		ResponsesPath:   protocolrouter.ResponsesPathRoot,
		Profile: protocolrouter.RequestProfile{
			ContentKinds: protocolrouter.ContentText,
		},
		Body: []byte(`{"model":"ernie-5.0","input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest responses: %v", err)
	}
	converted, err := NewProtocolRouter().Plan(responsesReq, snapshot)
	if err != nil {
		t.Fatalf("inbound Responses should convert to chat: %v", err)
	}
	if got, want := converted.TargetProtocol(), protocolrouter.ProtocolChatCompletions; got != want {
		t.Fatalf("converted target = %q, want %q", got, want)
	}
	if got, want := converted.Endpoint(), newapiintegration.QianfanBaseURL+"/v2/chat/completions"; got != want {
		t.Fatalf("converted endpoint = %q, want invented Responses URL %q", got, want)
	}
}

func TestProtocolAccountSnapshotOmitsUnresolvableClaimedResponses(t *testing.T) {
	account := qianfanChatTestAccount(90)
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses)
	snapshot, err := ProtocolAccountSnapshot(account, "ernie-5.0")
	if err != nil {
		t.Fatalf("claimed Responses must not fail Chat snapshot: %v", err)
	}
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolChatCompletions,
		RequestedModel:  "ernie-5.0",
		Profile: protocolrouter.RequestProfile{
			ContentKinds: protocolrouter.ContentText,
		},
		Body: []byte(`{"model":"ernie-5.0","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, want := plan.TargetProtocol(), protocolrouter.ProtocolChatCompletions; got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
	if got, want := plan.Endpoint(), newapiintegration.QianfanBaseURL+"/v2/chat/completions"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}

	responsesReq, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses,
		RequestedModel:  "ernie-5.0",
		ResponsesPath:   protocolrouter.ResponsesPathRoot,
		Profile: protocolrouter.RequestProfile{
			ContentKinds: protocolrouter.ContentText,
		},
		Body: []byte(`{"model":"ernie-5.0","input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest responses: %v", err)
	}
	converted, err := NewProtocolRouter().Plan(responsesReq, snapshot)
	if err != nil {
		t.Fatalf("inbound Responses should convert to chat, not invent /v1/responses: %v", err)
	}
	if got, want := converted.TargetProtocol(), protocolrouter.ProtocolChatCompletions; got != want {
		t.Fatalf("converted target = %q, want %q", got, want)
	}
	if got, want := converted.Endpoint(), newapiintegration.QianfanBaseURL+"/v2/chat/completions"; got != want {
		t.Fatalf("converted endpoint = %q, want invented Responses URL %q", got, want)
	}
}

func qianfanChatTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "百度",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": newapiintegration.QianfanBaseURL,
			"model_mapping": map[string]any{
				"ernie-5.0":     "ernie-5.0",
				"bge-large-zh":  "bge-large-zh",
				"deepseek-ocr":  "deepseek-ocr",
				"deepseek-v3.2": "deepseek-v3.2",
			},
		},
	}
}

func TestProtocolRoutingGovernsStableGeminiAccountShapes(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{
			name:    "antigravity oauth remains governed without current credentials",
			account: &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth},
			want:    true,
		},
		{
			name: "antigravity API-key upstream remains governed as a configurable upstream",
			account: &Account{Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": "https://upstream.example.test",
			}},
			want: true,
		},
		{
			name: "exact newapi vertex service account remains governed without current credentials",
			account: &Account{
				Platform:    PlatformNewAPI,
				Type:        AccountTypeServiceAccount,
				ChannelType: newapiconstant.ChannelTypeVertexAi,
			},
			want: true,
		},
		{
			name: "unrelated service account remains outside text governance",
			account: &Account{
				Platform:    PlatformNewAPI,
				Type:        AccountTypeServiceAccount,
				ChannelType: newapiconstant.ChannelTypeAws,
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := protocolRoutingGovernsAccount(tt.account); got != tt.want {
				t.Fatalf("protocolRoutingGovernsAccount() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProtocolAccountSnapshotDerivesGeminiEndpointProfile(t *testing.T) {
	tests := []struct {
		name         string
		account      *Account
		wantProfile  protocolrouter.GeminiEndpointProfile
		wantEndpoint string
	}{
		{
			name: "antigravity cloudcode",
			account: &Account{
				ID:       501,
				Platform: PlatformAntigravity,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"access_token":  "secret",
					"project_id":    "project-a",
					"model_mapping": map[string]any{"client-model": "gemini-2.5-pro"},
				},
				Extra: map[string]any{SupportedProtocolsExtraKey: []any{"gemini_generate_content"}},
			},
			wantProfile:  protocolrouter.GeminiEndpointAntigravityCloudCode,
			wantEndpoint: "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent",
		},
		{
			name: "newapi vertex service account",
			account: &Account{
				ID:          502,
				Platform:    PlatformNewAPI,
				Type:        AccountTypeServiceAccount,
				ChannelType: newapiconstant.ChannelTypeVertexAi,
				Credentials: map[string]any{
					"project_id":    "project-v",
					"location":      "us-central1",
					"model_mapping": map[string]any{"client-model": "gemini-2.5-pro"},
				},
				Extra: map[string]any{SupportedProtocolsExtraKey: []any{"gemini_generate_content"}},
			},
			wantProfile:  protocolrouter.GeminiEndpointVertexServiceAccount,
			wantEndpoint: "https://us-central1-aiplatform.googleapis.com/v1/projects/project-v/locations/us-central1/publishers/google/models/gemini-2.5-pro:generateContent",
		},
		{
			name: "antigravity API-key upstream uses mapped model",
			account: &Account{
				ID:       85,
				Platform: PlatformAntigravity,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":  "secret",
					"base_url": "https://upstream.example.test",
					"model_mapping": map[string]any{
						"client-model": "gemini-3.8-flash-medium",
					},
				},
				Extra: map[string]any{SupportedProtocolsExtraKey: []any{"gemini_generate_content"}},
			},
			wantProfile:  protocolrouter.GeminiEndpointAntigravityAPIKey,
			wantEndpoint: "https://upstream.example.test/v1beta/models/gemini-3.8-flash-medium:generateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachTestProtocolCapability(tt.account, protocolrouter.ProtocolGeminiGenerateContent)
			request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
				InboundProtocol: protocolrouter.ProtocolGeminiGenerateContent,
				RequestedModel:  "client-model",
				Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
				Body:            []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
			})
			if err != nil {
				t.Fatalf("NewCanonicalRequest: %v", err)
			}
			snapshot, err := protocolAccountSnapshotForRequest(tt.account, request)
			if err != nil {
				t.Fatalf("protocolAccountSnapshotForRequest: %v", err)
			}
			if snapshot.GeminiProfile() != tt.wantProfile {
				t.Fatalf("GeminiProfile = %q, want %q", snapshot.GeminiProfile(), tt.wantProfile)
			}
			exactModel := snapshot.ResolvedModel()
			if tt.wantProfile == protocolrouter.GeminiEndpointAntigravityAPIKey {
				if exactModel != "gemini-3.8-flash-medium" {
					t.Fatalf("ResolvedModel = %q, want mapped provider model", exactModel)
				}
			}
			endpoint, err := protocolGeminiExactEndpoint(tt.account, exactModel, snapshot.GeminiProfile(), request.Profile().Stream)
			if err != nil {
				t.Fatalf("protocolGeminiExactEndpoint: %v", err)
			}
			if endpoint != tt.wantEndpoint {
				t.Fatalf("endpoint = %q, want %q", endpoint, tt.wantEndpoint)
			}
		})
	}
}

func TestProtocolRouterRejectsResolvedModelOutsideOfficialRoutePolicy(t *testing.T) {
	account := &Account{
		ID:       78,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "oauth-secret",
			"model_mapping": map[string]any{"client-alias": "deepseek-v3"},
		},
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []any{"responses"},
		},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses,
		RequestedModel:  "client-alias",
		Profile: protocolrouter.RequestProfile{
			ContentKinds: protocolrouter.ContentText,
		},
		Body: []byte(`{"model":"client-alias","input":"hi"}`),
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	snapshot, err := ProtocolAccountSnapshot(account, request.RequestedModel())
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot: %v", err)
	}
	if snapshot.ResolvedModel() != "deepseek-v3" {
		t.Fatalf("resolved model = %q, want deepseek-v3", snapshot.ResolvedModel())
	}
	_, err = NewProtocolRouter().Plan(request, snapshot)
	if !errors.Is(err, protocolrouter.ErrModelNotAllowed) {
		t.Fatalf("Plan error = %v, want ErrModelNotAllowed", err)
	}
	if !errors.Is(err, protocolrouter.ErrNoLegalRoute) {
		t.Fatalf("Plan error = %v, want wrapped ErrNoLegalRoute", err)
	}
}

func TestSeedOfficialSupportedProtocolsIsConservativeAndIdempotent(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    []protocolrouter.Protocol
	}{
		{
			name: "Anthropic OAuth seeds messages",
			account: &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "secret"}},
			want: []protocolrouter.Protocol{protocolrouter.ProtocolMessages},
		},
		{
			name: "OpenAI OAuth seeds responses",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "secret"}},
			want: []protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		},
		{
			name: "custom API key is not inferred",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "secret", "base_url": "https://api.openai.com"}},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SeedOfficialSupportedProtocols(tt.account)
			SeedOfficialSupportedProtocols(tt.account)
			if got := tt.account.SupportedProtocols(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SupportedProtocols = %v, want %v", got, tt.want)
			}
		})
	}

	explicitEmpty := &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "secret"}}
	attachTestProtocolCapability(explicitEmpty)
	SeedOfficialSupportedProtocols(explicitEmpty)
	if got := explicitEmpty.SupportedProtocols(); len(got) != 0 {
		t.Fatalf("explicit empty capability was overwritten: %v", got)
	}
}

func TestAdminCreateCannotInjectSupportedProtocols(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	created, err := (&adminServiceImpl{accountRepo: repo}).CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "custom upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"api_key": "secret", "base_url": "https://relay.example.test/v1"},
		Extra:                map[string]any{SupportedProtocolsExtraKey: []any{"responses"}},
		SkipDefaultGroupBind: true,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, exists := created.Extra[SupportedProtocolsExtraKey]; exists {
		t.Fatalf("client injected canonical supported protocols: %#v", created.Extra[SupportedProtocolsExtraKey])
	}
}

func TestAdminUpdatePreservesCanonicalSupportedProtocolsAndIgnoresClientValue(t *testing.T) {
	accountID := int64(501)
	repo := &upstreamBillingProbeAdminRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{
			accountID: {
				ID:          accountID,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Credentials: map[string]any{"base_url": "https://relay.example.test/v1"},
				Extra: map[string]any{
					SupportedProtocolsExtraKey: []string{"messages"},
					"old_config":               true,
				},
			},
		},
	}}
	repo.accounts[accountID].ProtocolEndpointCapability = &ProtocolEndpointCapability{SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolMessages}}
	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []any{"responses"},
			"new_config":               true,
		},
	})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if got, want := updated.SupportedProtocols(), []protocolrouter.Protocol{protocolrouter.ProtocolMessages}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supported protocols = %v, want preserved %v", got, want)
	}
	if updated.Extra["new_config"] != true {
		t.Fatalf("unmanaged update was lost: extra=%#v", updated.Extra)
	}
}

func TestAdminUpdateExtraCannotWriteSupportedProtocols(t *testing.T) {
	accountID := int64(502)
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID:    accountID,
			Extra: map[string]any{SupportedProtocolsExtraKey: []string{"messages"}},
		},
	}}
	repo.accounts[accountID].ProtocolEndpointCapability = &ProtocolEndpointCapability{SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolMessages}}
	err := (&adminServiceImpl{accountRepo: repo}).UpdateAccountExtra(context.Background(), accountID, map[string]any{
		SupportedProtocolsExtraKey: []any{"responses"},
		"custom":                   true,
	})
	if err != nil {
		t.Fatalf("UpdateAccountExtra: %v", err)
	}
	account, err := repo.GetByID(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got, want := account.SupportedProtocols(), []protocolrouter.Protocol{protocolrouter.ProtocolMessages}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supported protocols = %v, want preserved %v", got, want)
	}
	if account.Extra["custom"] != true {
		t.Fatalf("unmanaged extra update was lost: %#v", account.Extra)
	}
}

func TestAntigravityAPIKeyKeepsChatWhenGeminiConversionCannotPreserve(t *testing.T) {
	account := &Account{ID: 62, Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"base_url": "https://upstream.example.test",
		"api_key":  "test",
		"model_mapping": map[string]any{
			"gemini-3.7-flash": "gemini-3.7-flash-high",
		},
	}}
	stored := []protocolrouter.Protocol{
		protocolrouter.ProtocolMessages,
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ProtocolGeminiGenerateContent,
	}
	attachTestProtocolCapability(account, stored...)

	convertibleBody := `{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`
	convertible, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gemini-3.7-flash", false, []byte(convertibleBody))
	if err != nil {
		t.Fatal(err)
	}
	if got := routingSupportedProtocolsForInbound(account, protocolrouter.ProtocolChatCompletions, &convertible); !reflect.DeepEqual(got, []protocolrouter.Protocol{protocolrouter.ProtocolGeminiGenerateContent}) {
		t.Fatalf("convertible chat must stay gemini-only for conversion-first: %v", got)
	}
	if got := routingSupportedProtocolsForInbound(account, protocolrouter.ProtocolMessages, nil); !reflect.DeepEqual(got, []protocolrouter.Protocol{protocolrouter.ProtocolGeminiGenerateContent}) {
		t.Fatalf("messages ingress must stay gemini-only: %v", got)
	}
	if !reflect.DeepEqual(account.SupportedProtocols(), stored) {
		t.Fatalf("capability owner list was rewritten: %v", account.SupportedProtocols())
	}

	cacheBody := `{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"session-1","max_tokens":16}`
	cacheReq, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gemini-3.7-flash", false, []byte(cacheBody))
	if err != nil {
		t.Fatal(err)
	}
	if cacheReq.Profile().PromptCache == protocolrouter.PromptCacheNone {
		t.Fatal("fixture must exercise prompt-cache")
	}
	if got := routingSupportedProtocolsForInbound(account, protocolrouter.ProtocolChatCompletions, &cacheReq); !reflect.DeepEqual(got, []protocolrouter.Protocol{protocolrouter.ProtocolGeminiGenerateContent}) {
		t.Fatalf("prompt-cache chat must stay gemini-only for Chat→Gemini: %v", got)
	}
	cacheSnapshot, err := protocolAccountSnapshotForRequest(account, cacheReq)
	if err != nil {
		t.Fatal(err)
	}
	cachePlan, err := NewProtocolRouter().Plan(cacheReq, cacheSnapshot)
	if err != nil {
		t.Fatalf("prompt-cache chat must plan Chat→Gemini: %v", err)
	}
	if cachePlan.TargetProtocol() != protocolrouter.ProtocolGeminiGenerateContent || cachePlan.AdapterID() != protocolrouter.AdapterChatToGemini {
		t.Fatalf("prompt-cache plan target=%s adapter=%s, want chat→gemini", cachePlan.TargetProtocol(), cachePlan.AdapterID())
	}

	// Forced tool_choice cannot Chat→Gemini; admit verified chat hop so identity remains eligible.
	body := `{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required"}`
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gemini-3.7-flash", false, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if protocolrouter.OpenAIToGeminiConversionPreserves(protocolrouter.ProtocolChatCompletions, request) {
		t.Fatal("fixture must exercise a Chat→Gemini-illegal tool_choice")
	}
	got := routingSupportedProtocolsForInbound(account, protocolrouter.ProtocolChatCompletions, &request)
	want := []protocolrouter.Protocol{
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ProtocolGeminiGenerateContent,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routingSupportedProtocolsForInbound(non-convertible chat) = %v, want %v", got, want)
	}
	snapshot, err := protocolAccountSnapshotForRequest(account, request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	if err != nil {
		t.Fatalf("non-convertible chat must admit native chat hop: %v", err)
	}
	if plan.TargetProtocol() != protocolrouter.ProtocolChatCompletions || plan.AdapterID() != protocolrouter.AdapterChatIdentity {
		t.Fatalf("plan target=%s adapter=%s, want chat identity", plan.TargetProtocol(), plan.AdapterID())
	}

	convertibleSnapshot, err := protocolAccountSnapshotForRequest(account, convertible)
	if err != nil {
		t.Fatal(err)
	}
	convertiblePlan, err := NewProtocolRouter().Plan(convertible, convertibleSnapshot)
	if err != nil {
		t.Fatalf("convertible chat must still plan Chat→Gemini: %v", err)
	}
	if convertiblePlan.TargetProtocol() != protocolrouter.ProtocolGeminiGenerateContent || convertiblePlan.AdapterID() != protocolrouter.AdapterChatToGemini {
		t.Fatalf("convertible plan target=%s adapter=%s, want chat→gemini", convertiblePlan.TargetProtocol(), convertiblePlan.AdapterID())
	}
}

func applySupportedProtocolsUpdate(account *Account, update map[string]any) {
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[SupportedProtocolsExtraKey] = update[SupportedProtocolsExtraKey]
}
