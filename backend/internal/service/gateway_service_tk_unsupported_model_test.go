//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTkSelectionFailedDueToUnsupportedModel(t *testing.T) {
	cases := []struct {
		name  string
		stats selectionFailureStats
		want  bool
	}{
		{
			name:  "pure unsupported model -> true",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 5},
			want:  true,
		},
		{
			name:  "unsupported plus a model-rate-limited candidate -> false (capacity)",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 4, ModelRateLimited: 1},
			want:  false,
		},
		{
			name:  "unsupported plus an unschedulable supporting candidate -> false (capacity after recovery)",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 4, Unschedulable: 1},
			want:  false,
		},
		{
			name:  "unsupported candidates stay unsupported even when some are unschedulable",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 5},
			want:  true,
		},
		{
			name:  "an eligible candidate exists -> false",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 4, Eligible: 1},
			want:  false,
		},
		{
			name:  "no model-unsupported at all -> false",
			stats: selectionFailureStats{Total: 5, Unschedulable: 5},
			want:  false,
		},
		{
			name:  "empty stats -> false",
			stats: selectionFailureStats{},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkSelectionFailedDueToUnsupportedModel(tc.stats); got != tc.want {
				t.Fatalf("tkSelectionFailedDueToUnsupportedModel(%+v) = %v, want %v", tc.stats, got, tc.want)
			}
		})
	}
}

func TestTkWrapSelectionFailure(t *testing.T) {
	t.Run("empty model returns bare ErrNoAvailableAccounts", func(t *testing.T) {
		err := tkWrapSelectionFailure(PlatformAnthropic, "", selectionFailureStats{Total: 3, ModelUnsupported: 3})
		if !errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("want ErrNoAvailableAccounts, got %v", err)
		}
		if errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("empty model must not be classified as unsupported model: %v", err)
		}
	})

	t.Run("pure unsupported model returns ErrUnsupportedModel with model name", func(t *testing.T) {
		err := tkWrapSelectionFailure(PlatformAnthropic, "opus", selectionFailureStats{Total: 5, ModelUnsupported: 5})
		if !errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("want ErrUnsupportedModel, got %v", err)
		}
		if errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("unsupported model must be distinct from ErrNoAvailableAccounts: %v", err)
		}
		if !strings.Contains(err.Error(), "opus") {
			t.Fatalf("error should carry the model name, got %q", err.Error())
		}
		// Must NOT contain the "no available accounts" phrase, or
		// handler.isOpsNoAvailableAccountError would mislabel it as routing-capacity.
		if strings.Contains(strings.ToLower(err.Error()), "no available accounts") {
			t.Fatalf("unsupported-model message must not contain 'no available accounts': %q", err.Error())
		}
	})

	t.Run("capacity failure returns ErrNoAvailableAccounts (not unsupported)", func(t *testing.T) {
		err := tkWrapSelectionFailure(PlatformAnthropic, "claude-opus-4-8", selectionFailureStats{Total: 5, ModelUnsupported: 4, ModelRateLimited: 1})
		if !errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("want ErrNoAvailableAccounts, got %v", err)
		}
		if errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("capacity failure must not be classified as unsupported model: %v", err)
		}
	})

	t.Run("cross-vendor model beats mixed stats routing 429", func(t *testing.T) {
		stats := selectionFailureStats{
			Total:            5,
			ModelUnsupported: 4,
			Unschedulable:    1,
		}
		err := tkWrapSelectionFailure(PlatformAnthropic, "gpt", stats)
		if !errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("want ErrUnsupportedModel for cross-vendor name, got %v", err)
		}
		if errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("cross-vendor must not fall through to empty pool: %v", err)
		}
	})

	t.Run("antigravity empty pool stays capacity-owned and does not populate unsupported cache", func(t *testing.T) {
		err := tkWrapSelectionFailure(PlatformAntigravity, "gemini-3-flash", selectionFailureStats{})
		if !errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("want ErrNoAvailableAccounts for an empty Antigravity pool, got %v", err)
		}
		if errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("Antigravity model must not be checked against the Anthropic namespace: %v", err)
		}

		cache := newTkGroupUnsupportedModelNegativeCache()
		groupID := int64(17)
		_ = tkGroupUnsupportedModelRecordErr(cache, &groupID, "gemini-3-flash", err)
		if cache.get(groupID, "gemini-3-flash") {
			t.Fatal("capacity failure must not populate the unsupported-model negative cache")
		}
	})
}

func TestSelectAccountWithLoadAwareness_AntigravityEmptyPoolStaysCapacityOwned(t *testing.T) {
	groupID := int64(85)
	unsupportedCache := newTkGroupUnsupportedModelNegativeCache()
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true

	svc := &GatewayService{
		accountRepo: &mockAccountRepoForPlatform{},
		groupRepo: &mockGroupRepoForGateway{
			groups: map[int64]*Group{
				groupID: {
					ID:       groupID,
					Platform: PlatformAntigravity,
					Status:   StatusActive,
					Hydrated: true,
				},
			},
		},
		cache:                   &mockGatewayCacheForPlatform{},
		cfg:                     cfg,
		concurrencyService:      NewConcurrencyService(&mockConcurrencyCache{}),
		tkGroupUnsupportedCache: unsupportedCache,
	}

	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gemini-3-flash", nil, "", 0)
	if result != nil {
		t.Fatalf("expected no selection, got %+v", result)
	}
	if !errors.Is(err, ErrNoAvailableAccounts) {
		t.Fatalf("want ErrNoAvailableAccounts, got %v", err)
	}
	if errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("Antigravity empty pool must remain capacity-owned: %v", err)
	}
	if unsupportedCache.get(groupID, "gemini-3-flash") {
		t.Fatal("capacity failure must not populate the unsupported-model negative cache")
	}
}

func TestTkIsAnthropicCrossVendorModelName(t *testing.T) {
	if !TkIsAnthropicCrossVendorModelName("gpt") {
		t.Fatal("gpt must be cross-vendor on anthropic ingress")
	}
	if !TkIsAnthropicCrossVendorModelName("deepseek-v4-flash") {
		t.Fatal("deepseek must be cross-vendor on anthropic ingress")
	}
	if TkIsAnthropicCrossVendorModelName("claude-opus-4-8") {
		t.Fatal("claude-opus-4-8 must not be cross-vendor")
	}
	if TkIsAnthropicCrossVendorModelName("") {
		t.Fatal("empty model is out of scope for cross-vendor ingress")
	}
}

func TestDiagnoseSelectionFailure_ModelUnsupportedPrecedesUnschedulable(t *testing.T) {
	svc := &GatewayService{}
	unsupportedUnsched := &Account{
		ID:          1,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: false,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
		},
	}
	got := svc.diagnoseSelectionFailure(nil, unsupportedUnsched, "claude-fable-5", PlatformAnthropic, nil, false)
	if got.Category != "model_unsupported" {
		t.Fatalf("unsupported unschedulable account misclassified: got=%+v", got)
	}

	supportingUnsched := &Account{
		ID:          2,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: false,
		Credentials: map[string]any{
			"base_url": "https://api-us3.tokenkey.dev",
		},
	}
	got = svc.diagnoseSelectionFailure(nil, supportingUnsched, "claude-opus-4-8", PlatformAnthropic, nil, false)
	if got.Category != "unschedulable" {
		t.Fatalf("supporting unschedulable account must stay capacity-owned: got=%+v", got)
	}
}

// Tk cross-vendor dirty-model guard (prod 2026-06-16, edge us3 oh1-ls-b ID 4):
// a passthrough anthropic OAuth account forwarded non-claude names
// (deepseek-v4-flash) to api.anthropic.com → 404 + abuse fingerprint. The guard
// converts those to a local Path A 400 (selection miss) while leaving same-family
// names (claude-haiku-4-6, tolerated upstream) and all non-anthropic platforms
// untouched.
func TestTkIsForwardableAnthropicModelName(t *testing.T) {
	forwardable := []string{
		"claude-opus-4-8",
		"claude-haiku-4-6",           // same-family stale/typo: intentionally allowed (upstream tolerates)
		"claude-sonnet-4-5-20250929", // dated snapshot
		"Claude-Opus-4-8",            // case-insensitive
		" claude-opus-4-8 ",          // trimmed
		"",                           // empty out of scope → allowed
	}
	for _, m := range forwardable {
		if !tkIsForwardableAnthropicModelName(m) {
			t.Errorf("tkIsForwardableAnthropicModelName(%q) = false, want true", m)
		}
	}

	blocked := []string{
		"deepseek-v4-flash", // the revoked account's cross-vendor leak
		"gpt-4o",
		"gemini-2.5-pro",
		"qwen-max",
		"opus", // bare family (aliased upstream at handler throat; here it is not claude-*)
	}
	for _, m := range blocked {
		if tkIsForwardableAnthropicModelName(m) {
			t.Errorf("tkIsForwardableAnthropicModelName(%q) = true, want false", m)
		}
	}
}

func TestIsModelSupportedByAccount_TkDirtyModelGuard(t *testing.T) {
	svc := &GatewayService{}

	// anthropic OAuth passthrough (empty model_mapping): cross-vendor blocked,
	// real claude served, same-family stale name still forwards (Path B, tolerated).
	anthropicOAuth := &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	if svc.isModelSupportedByAccount(anthropicOAuth, "deepseek-v4-flash") {
		t.Error("anthropic OAuth passthrough should NOT support deepseek-v4-flash (cross-vendor leak)")
	}
	if !svc.isModelSupportedByAccount(anthropicOAuth, "claude-opus-4-8") {
		t.Error("anthropic OAuth passthrough should support claude-opus-4-8")
	}
	if !svc.isModelSupportedByAccount(anthropicOAuth, "claude-haiku-4-6") {
		t.Error("anthropic OAuth passthrough should still allow claude-haiku-4-6 (same-family, tolerated)")
	}

	// anthropic APIKey passthrough also forwards to api.anthropic.com → same guard.
	anthropicAPIKey := &Account{ID: 5, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	if svc.isModelSupportedByAccount(anthropicAPIKey, "deepseek-v4-flash") {
		t.Error("anthropic APIKey passthrough should NOT support deepseek-v4-flash")
	}

	// anthropic ServiceAccount (Vertex): upstream is NOT api.anthropic.com; guard
	// must NOT apply — a claude name stays supported.
	anthropicVertex := &Account{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeServiceAccount}
	if !svc.isModelSupportedByAccount(anthropicVertex, "claude-sonnet-4-5") {
		t.Error("anthropic ServiceAccount (Vertex) should still support claude-sonnet-4-5")
	}

	// Non-anthropic platforms untouched: passthrough openai / newapi keep multi-vendor.
	openai := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	if !svc.isModelSupportedByAccount(openai, "deepseek-v4-flash") {
		t.Error("openai passthrough must still support arbitrary models (not anthropic-scoped)")
	}
	newapi := &Account{ID: 8, Platform: PlatformNewAPI, Type: AccountTypeAPIKey}
	if !svc.isModelSupportedByAccount(newapi, "deepseek-v4-flash") {
		t.Error("newapi (fifth platform, multi-vendor) must still support deepseek-v4-flash")
	}
}

func TestIsModelSupportedByAccount_TkGuardWithExplicitMapping(t *testing.T) {
	svc := &GatewayService{}
	// An anthropic account WITH an explicit model_mapping is constrained by the
	// mapping; the namespace guard (passthrough-only) does not run for it.
	mapped := &Account{
		ID:       9,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"claude-opus-4-8": "claude-opus-4-8"},
		},
	}
	if !svc.isModelSupportedByAccount(mapped, "claude-opus-4-8") {
		t.Error("mapped anthropic account should support its mapped claude model")
	}
	if svc.isModelSupportedByAccount(mapped, "deepseek-v4-flash") {
		t.Error("mapped anthropic account should not support an unmapped cross-vendor model")
	}

	// Guard must NOT run for a mapped account: a non-claude REQUEST name that the
	// account explicitly maps to a claude model is served — the forwarded model is
	// the mapped claude name (no leak), so the passthrough-only guard does not block
	// it. (Regression for the over-block where the guard short-circuited before the
	// mapping was consulted.)
	foreignToClaude := &Account{
		ID:       10,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "claude-sonnet-4-5"},
		},
	}
	if !svc.isModelSupportedByAccount(foreignToClaude, "gpt-4o") {
		t.Error("anthropic account mapping gpt-4o->claude-sonnet-4-5 should support gpt-4o (forwarded model is claude, no leak)")
	}
	// A different non-claude name not in the mapping is still unsupported (mapping is
	// the allowlist) — unchanged behavior.
	if svc.isModelSupportedByAccount(foreignToClaude, "deepseek-v4-flash") {
		t.Error("mapped account should not support a cross-vendor model absent from its mapping")
	}
}
