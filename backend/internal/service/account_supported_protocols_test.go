package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
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

func TestAccountSupportedProtocolsReadsOnlyCanonicalExtraKey(t *testing.T) {
	account := &Account{Extra: map[string]any{
		SupportedProtocolsExtraKey:         []any{"responses", "messages", "responses"},
		"openai_responses_supported":       true,
		"openai_native_messages_supported": true,
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
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []any{"chat_completions", "responses"},
		},
	}

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
		t.Fatal("credential change did not change account revision")
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
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []any{"responses"},
		},
	}
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
			SupportedProtocolsExtraKey: []any{"messages"},
			"custom_base_url_enabled":  true,
			"custom_base_url":          "https://relay.example.test/v1",
		},
	}
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

	explicitEmpty := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		SupportedProtocolsExtraKey: []any{},
	}}
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
