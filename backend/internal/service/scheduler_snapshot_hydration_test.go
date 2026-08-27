//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

type snapshotHydrationCache struct {
	snapshot []*Account
	accounts map[int64]*Account
}

func (c *snapshotHydrationCache) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	return c.snapshot, true, nil
}

func (c *snapshotHydrationCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *snapshotHydrationCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) error {
	return nil
}

func (c *snapshotHydrationCache) RetireBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *snapshotHydrationCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *snapshotHydrationCache) TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	return SchedulerGroupLifecycleLease{}, false, nil
}

func (c *snapshotHydrationCache) ReleaseGroupLifecycleLease(context.Context, SchedulerGroupLifecycleLease) error {
	return nil
}

func (c *snapshotHydrationCache) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	if c.accounts == nil {
		return nil, nil
	}
	return c.accounts[accountID], nil
}

func (c *snapshotHydrationCache) SetAccount(ctx context.Context, account *Account) error {
	return nil
}

func (c *snapshotHydrationCache) DeleteAccount(ctx context.Context, accountID int64) error {
	return nil
}

func (c *snapshotHydrationCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return nil
}

func (c *snapshotHydrationCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	return true, nil
}

func (c *snapshotHydrationCache) UnlockBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *snapshotHydrationCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	return nil, nil
}

func (c *snapshotHydrationCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	return 0, nil
}

func (c *snapshotHydrationCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	return nil
}

func TestOpenAISelectAccountWithLoadAwareness_HydratesSelectedAccountFromSchedulerSnapshot(t *testing.T) {
	cache := &snapshotHydrationCache{
		snapshot: []*Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    1,
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"gpt-4": "gpt-4",
					},
				},
			},
		},
		accounts: map[int64]*Account{
			1: {
				ID:          1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    1,
				Credentials: map[string]any{
					"api_key":       "sk-live",
					"model_mapping": map[string]any{"gpt-4": "gpt-4"},
				},
			},
		},
	}

	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	groupID := int64(2)
	svc := &OpenAIGatewayService{
		schedulerSnapshot: schedulerSnapshot,
		cache:             &stubGatewayCache{},
	}

	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectAccountWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Account == nil {
		t.Fatalf("expected selected account")
	}
	if got := selection.Account.GetOpenAIApiKey(); got != "sk-live" {
		t.Fatalf("expected hydrated api key, got %q", got)
	}
}

func TestOpenAINewAcquiredSelectionResult_ReleasesSlotWhenHydrationFails(t *testing.T) {
	cache := &snapshotHydrationCache{
		accounts: map[int64]*Account{},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, stubOpenAIAccountRepo{}, nil, nil)
	svc := &OpenAIGatewayService{
		schedulerSnapshot: schedulerSnapshot,
	}
	releaseCalls := 0

	selection, err := svc.newAcquiredSelectionResult(context.Background(), &Account{ID: 1001}, func() {
		releaseCalls++
	})

	if err == nil {
		t.Fatalf("expected hydration error")
	}
	if selection != nil {
		t.Fatalf("expected nil selection on hydration error")
	}
	if releaseCalls != 1 {
		t.Fatalf("expected release to be called once, got %d", releaseCalls)
	}
}

func TestGatewayNewSelectionResult_ReleasesSlotWhenHydrationFails(t *testing.T) {
	cache := &snapshotHydrationCache{
		accounts: map[int64]*Account{},
	}
	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, stubOpenAIAccountRepo{}, nil, nil)
	svc := &GatewayService{
		schedulerSnapshot: schedulerSnapshot,
	}
	releaseCalls := 0

	selection, err := svc.newSelectionResult(context.Background(), &Account{ID: 1002}, true, func() {
		releaseCalls++
	}, nil)

	if err == nil {
		t.Fatalf("expected hydration error")
	}
	if selection != nil {
		t.Fatalf("expected nil selection on hydration error")
	}
	if releaseCalls != 1 {
		t.Fatalf("expected release to be called once, got %d", releaseCalls)
	}
}

func TestGatewayNewSelectionResult_AttachesMetadataPlanAfterCredentialHydration(t *testing.T) {
	updatedAt := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	vertexServiceAccount := `{"type":"service_account","project_id":"vertex-project","private_key":"private-key","client_email":"svc@vertex-project.iam.gserviceaccount.com"}`
	tests := []struct {
		name     string
		model    string
		protocol protocolrouter.Protocol
		full     *Account
		metadata *Account
	}{
		{
			name:     "api key",
			model:    "gpt-5.4",
			protocol: protocolrouter.ProtocolChatCompletions,
			full: &Account{
				ID: 2001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"api_key": "secret", "base_url": "https://relay.example.test/v1"}, UpdatedAt: updatedAt,
			},
			metadata: &Account{
				ID: 2001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"base_url": "https://relay.example.test/v1", ProtocolAuthorizationSnapshotCredentialKey: true}, UpdatedAt: updatedAt,
			},
		},
		{
			name:     "oauth",
			model:    "gpt-5.4",
			protocol: protocolrouter.ProtocolResponses,
			full: &Account{
				ID: 2002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"access_token": "secret"}, UpdatedAt: updatedAt,
			},
			metadata: &Account{
				ID: 2002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{ProtocolAuthorizationSnapshotCredentialKey: true}, UpdatedAt: updatedAt,
			},
		},
		{
			name:     "antigravity",
			model:    "gemini-2.5-flash",
			protocol: protocolrouter.ProtocolGeminiGenerateContent,
			full: &Account{
				ID: 2003, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"access_token": "secret", "project_id": "ag-project", "model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}, UpdatedAt: updatedAt,
			},
			metadata: &Account{
				ID: 2003, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{ProtocolAuthorizationSnapshotCredentialKey: true, "project_id": "ag-project", "model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}, UpdatedAt: updatedAt,
			},
		},
		{
			name:     "vertex service account",
			model:    "gemini-2.5-flash",
			protocol: protocolrouter.ProtocolGeminiGenerateContent,
			full: &Account{
				ID: 2004, Platform: PlatformNewAPI, Type: AccountTypeServiceAccount, ChannelType: newapiconstant.ChannelTypeVertexAi, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"service_account_json": vertexServiceAccount, "location": "us-central1", "model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}, UpdatedAt: updatedAt,
			},
			metadata: &Account{
				ID: 2004, Platform: PlatformNewAPI, Type: AccountTypeServiceAccount, ChannelType: newapiconstant.ChannelTypeVertexAi, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{ProtocolAuthorizationSnapshotCredentialKey: true, "project_id": "vertex-project", "location": "us-central1", "model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}, UpdatedAt: updatedAt,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachTestProtocolCapability(tt.full, tt.protocol)
			capability := *tt.full.ProtocolEndpointCapability
			tt.metadata.ProtocolEndpointCapabilityID = tt.full.ProtocolEndpointCapabilityID
			tt.metadata.ProtocolEndpointCapability = &capability
			request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
				InboundProtocol: protocolrouter.ProtocolMessages,
				RequestedModel:  tt.model,
				Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
				Body:            []byte(`{"model":"` + tt.model + `","messages":[{"role":"user","content":"hi"}]}`),
			})
			if err != nil {
				t.Fatalf("NewCanonicalRequest: %v", err)
			}
			ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
			if !ProtocolRouteLegal(ctx, tt.metadata, tt.model) {
				t.Fatal("scheduler metadata rejected a legal route")
			}
			cache := &snapshotHydrationCache{accounts: map[int64]*Account{tt.full.ID: tt.full}}
			svc := &GatewayService{schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil)}

			selection, err := svc.newSelectionResult(ctx, tt.metadata, false, nil, nil)
			if err != nil {
				t.Fatalf("newSelectionResult after hydration: %v", err)
			}
			plan, ok := ProtocolPlanFromSelection(selection)
			if !ok || plan.AccountID() != tt.full.ID || plan.TargetProtocol() != tt.protocol {
				t.Fatalf("hydrated selection plan = account %d target %q", plan.AccountID(), plan.TargetProtocol())
			}
		})
	}
}

func TestGatewaySelectAccountWithLoadAwareness_HydratesSelectedAccountFromSchedulerSnapshot(t *testing.T) {
	cache := &snapshotHydrationCache{
		snapshot: []*Account{
			{
				ID:          9,
				Platform:    PlatformAnthropic,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    1,
			},
		},
		accounts: map[int64]*Account{
			9: {
				ID:          9,
				Platform:    PlatformAnthropic,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    1,
				Credentials: map[string]any{
					"api_key": "anthropic-live-key",
				},
			},
		},
	}

	schedulerSnapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	svc := &GatewayService{
		schedulerSnapshot: schedulerSnapshot,
		cache:             &mockGatewayCacheForPlatform{},
		cfg:               testConfig(),
	}

	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "claude-3-5-sonnet-20241022", nil, "", 0)
	if err != nil {
		t.Fatalf("SelectAccountWithLoadAwareness error: %v", err)
	}
	if result == nil || result.Account == nil {
		t.Fatalf("expected selected account")
	}
	if got := result.Account.GetCredential("api_key"); got != "anthropic-live-key" {
		t.Fatalf("expected hydrated api key, got %q", got)
	}
}

func TestGatewaySelectAccountWithLoadAwareness_SkipsAntigravityGeminiFamilyRateLimitedSnapshot(t *testing.T) {
	resetAt := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	cache := &snapshotHydrationCache{
		snapshot: []*Account{
			{
				ID:          1,
				Platform:    PlatformAntigravity,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    1,
				AccountGroups: []AccountGroup{
					{AccountID: 1, GroupID: 22},
				},
				GroupIDs: []int64{22},
				Extra: map[string]any{
					"mixed_scheduling": true,
					modelRateLimitsKey: map[string]any{
						antigravityGeminiModelRateLimitKey: map[string]any{
							"rate_limit_reset_at": resetAt,
						},
					},
				},
			},
			{
				ID:          2,
				Platform:    PlatformAntigravity,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    2,
				AccountGroups: []AccountGroup{
					{AccountID: 2, GroupID: 22},
				},
				GroupIDs: []int64{22},
				Extra: map[string]any{
					"mixed_scheduling": true,
				},
			},
		},
		accounts: map[int64]*Account{
			1: {ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth},
			2: {ID: 2, Platform: PlatformAntigravity, Type: AccountTypeOAuth},
		},
	}
	groupID := int64(22)
	svc := &GatewayService{
		schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, nil, nil, nil),
		groupRepo: &mockGroupRepoForGateway{
			groups: map[int64]*Group{
				groupID: {
					ID:       groupID,
					Platform: PlatformGemini,
					Status:   StatusActive,
					Hydrated: true,
				},
			},
		},
		concurrencyService: NewConcurrencyService(&mockConcurrencyCache{}),
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				Scheduling: config.GatewaySchedulingConfig{
					LoadBatchEnabled:         true,
					StickySessionMaxWaiting:  3,
					StickySessionWaitTimeout: time.Second,
					FallbackWaitTimeout:      time.Second,
					FallbackMaxWaiting:       10,
				},
			},
		},
	}

	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gemini-3-flash", nil, "", 0)
	if err != nil {
		t.Fatalf("SelectAccountWithLoadAwareness error: %v", err)
	}
	if result == nil || result.Account == nil {
		t.Fatalf("expected selected account")
	}
	if result.Account.ID != 2 {
		t.Fatalf("expected scheduler to skip Gemini-family limited antigravity account 1, got %d", result.Account.ID)
	}
}
