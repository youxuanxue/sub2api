//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The reason string and the bool predicate must agree for every input: the bool
// delegates to the reason function, so a disagreement would mean the diagnostic
// log names a gate that did not actually reject the account (or stays silent
// about one that did). 2026-08-25: the absence of any such reason is what made
// the edge us6 outage take a day to attribute — every branch reported the same
// bare "unschedulable".
func TestEligibilityReasonAgreesWithPredicate(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		account *Account
		model   string
	}{
		{
			name:    "nil account",
			account: nil,
		},
		{
			name: "healthy openai oauth",
			account: &Account{
				ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true,
			},
			model: "gpt-5.6-sol",
		},
		{
			name: "not schedulable",
			account: &Account{
				ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: false,
			},
			model: "gpt-5.6-sol",
		},
		{
			name: "disabled account",
			account: &Account{
				ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusDisabled, Schedulable: true,
			},
			model: "gpt-5.6-sol",
		},
		{
			name: "foreign platform for an openai pool",
			account: &Account{
				ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true,
			},
			model: "gpt-5.6-sol",
		},
		{
			name: "oauth account asked for a foreign model",
			account: &Account{
				ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true,
			},
			model: "qwen3-8b",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := openAICompatEligibilityReason(ctx, tc.account, PlatformOpenAI, tc.model, false, "")
			eligible := isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, tc.account, PlatformOpenAI, tc.model, false, "")
			require.Equal(t, reason == "", eligible,
				"reason=%q must be empty exactly when the predicate passes (got eligible=%v)", reason, eligible)
		})
	}
}

// Each rejecting branch must name ITSELF, not a shared placeholder. A bare
// "unschedulable" cannot be acted on; "not_pool_member(account_platform=...)"
// can.
func TestEligibilityReasonNamesTheFailingGate(t *testing.T) {
	ctx := context.Background()

	nilReason := openAICompatEligibilityReason(ctx, nil, PlatformOpenAI, "gpt-5.6-sol", false, "")
	require.Equal(t, openAICompatIneligibleAccountNil, nilReason)

	foreignPlatform := &Account{
		ID: 10, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
	}
	reason := openAICompatEligibilityReason(ctx, foreignPlatform, PlatformOpenAI, "gpt-5.6-sol", false, "")
	require.True(t, strings.HasPrefix(reason, openAICompatIneligibleNotPoolMember),
		"want a not_pool_member reason, got %q", reason)
	require.Contains(t, reason, PlatformAnthropic,
		"the reason must name the offending platform so the reader need not look it up")

	notSchedulable := &Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: false,
	}
	require.Equal(t, openAICompatIneligibleAccountCooling,
		openAICompatEligibilityReason(ctx, notSchedulable, PlatformOpenAI, "gpt-5.6-sol", false, ""))

	foreignModel := &Account{
		ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
	}
	require.Equal(t, openAICompatIneligibleModelUnsupported,
		openAICompatEligibilityReason(ctx, foreignModel, PlatformOpenAI, "qwen3-8b", false, ""),
		"an openai oauth account cannot serve a foreign vendor model")

	// Every reason code must be a non-empty, whitespace-free token so log
	// consumers can group on it.
	for _, code := range []string{
		openAICompatIneligibleAccountNil,
		openAICompatIneligibleNotPoolMember,
		openAICompatIneligibleNotCompatible,
		openAICompatIneligibleAccountCooling,
		openAICompatIneligibleQuotaAutoPause,
		openAICompatIneligibleModelUnsupported,
		openAICompatIneligibleCapabilityMissing,
		openAICompatIneligibleCompactMissing,
		openAICompatIneligibleNoLegalRoute,
	} {
		require.NotEmpty(t, code)
		require.NotContains(t, code, " ", "reason code %q must be a single token", code)
	}
}

// An ungoverned account must not be reported as route-illegal: protocol routing
// only governs requests that carry a routing context, and a false positive here
// would blame the router for every ordinary rejection.
func TestProtocolRouteIllegalReasonEmptyWhenUngoverned(t *testing.T) {
	acc := &Account{
		ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
	}
	require.Empty(t, tkProtocolRouteIllegalReason(context.Background(), acc, "gpt-5.6-sol"),
		"no routing context in ctx => not governed => no route reason")
}

// The unschedulable bucket must carry per-account reason samples, capped like
// the sibling sample helpers so a large pool cannot flood one log line.
func TestAppendSelectionFailureReasonSampleIsCapped(t *testing.T) {
	var samples []string
	for i := int64(1); i <= 10; i++ {
		samples = appendSelectionFailureReasonSample(samples, i, "no_legal_protocol_route")
	}
	require.Len(t, samples, 5, "reason samples must be capped at 5 like the other buckets")
	require.Equal(t, "1(no_legal_protocol_route)", samples[0],
		"a sample must pair the account id with its reason")

	// An empty reason still records the account, so the count never disagrees
	// with the bucket total.
	require.Equal(t, "7(unspecified)", appendSelectionFailureReasonSample(nil, 7, "")[0])
}
