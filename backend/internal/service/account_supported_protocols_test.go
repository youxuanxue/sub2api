package service

import (
	"context"
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

func TestProtocolAccountSnapshotResolvesModelAndRevisionFromAccountFacts(t *testing.T) {
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
	if snapshot.Revision() == "" {
		t.Fatal("snapshot revision is empty")
	}

	changed := *account
	changed.UpdatedAt = updatedAt.Add(time.Nanosecond)
	changed.Credentials = map[string]any{
		"api_key":       "different-secret",
		"base_url":      "https://relay.example.test/v1",
		"model_mapping": map[string]any{"client-model": "upstream-model"},
	}
	changedSnapshot, err := ProtocolAccountSnapshot(&changed, "client-model")
	if err != nil {
		t.Fatalf("ProtocolAccountSnapshot changed: %v", err)
	}
	if changedSnapshot.Revision() == snapshot.Revision() {
		t.Fatal("persisted account update did not change account revision")
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
			name: "antigravity edge relay remains governed as a configurable upstream",
			account: &Account{Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": "https://api-us3.tokenkey.dev",
			}},
			want: true,
		},
		{
			name: "arbitrary antigravity apikey account remains outside the stable relay shape",
			account: &Account{Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": "https://relay.example.test",
			}},
			want: false,
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
			endpoint, err := protocolGeminiExactEndpoint(tt.account, snapshot.ResolvedModel(), snapshot.GeminiProfile(), request.Profile().Stream)
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
	if _, err := NewProtocolRouter().Plan(request, snapshot); err != protocolrouter.ErrNoLegalRoute {
		t.Fatalf("Plan error = %v, want ErrNoLegalRoute", err)
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
				ID:       accountID,
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Status:   StatusActive,
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
