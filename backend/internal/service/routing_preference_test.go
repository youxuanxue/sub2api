//go:build unit

package service

import "testing"

func TestCandidatePathLessTokenseaPreferenceOverridesGlobalPriority(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	tokensea := &candidateExecutionPath{account: &Account{ID: 136, Platform: PlatformAnthropic, Priority: 20}, model: "claude-sonnet-4-6"}
	other := &candidateExecutionPath{account: &Account{ID: 300, Platform: PlatformAnthropic, Priority: 1}, model: "claude-sonnet-4-6"}

	if !candidatePathLess(key, nil, nil, tokensea, other) {
		t.Fatal("tokensea preference must outrank a lower global priority account")
	}
}

func TestCandidatePathLessDefaultKeepsGlobalPriority(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceDefault}
	preferred := &candidateExecutionPath{account: &Account{ID: 136, Platform: PlatformAnthropic, Priority: 20}, model: "claude-sonnet-4-6"}
	other := &candidateExecutionPath{account: &Account{ID: 300, Platform: PlatformAnthropic, Priority: 1}, model: "claude-sonnet-4-6"}

	if candidatePathLess(key, nil, nil, preferred, other) {
		t.Fatal("default preference must preserve global priority")
	}
}

func TestCandidatePreferenceOnlyMatchesConfiguredTokenseaAccounts(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	if candidatePreferenceRank(key, &Account{ID: 136, Platform: PlatformAnthropic}, "claude-sonnet-4-6") != 0 {
		t.Fatal("account 136 must be preferred")
	}
	if candidatePreferenceRank(key, &Account{ID: 218, Platform: PlatformAnthropic}, "claude-sonnet-4-6") != 0 {
		t.Fatal("account 218 must be preferred")
	}
	if candidatePreferenceRank(key, &Account{ID: 219, Platform: PlatformAnthropic}, "claude-sonnet-4-6") != 1 {
		t.Fatal("unconfigured account must remain in fallback tier")
	}
}

func TestCandidatePreferenceOnlyAppliesToAnthropicClaude(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	if candidatePreferenceRank(key, &Account{ID: 136, Platform: PlatformAnthropic}, "gpt-5.4") != 1 {
		t.Fatal("tokensea preference must not affect non-Claude models")
	}
	if candidatePreferenceRank(key, &Account{ID: 136, Platform: PlatformOpenAI}, "claude-sonnet-4-6") != 1 {
		t.Fatal("tokensea preference must not affect non-Anthropic accounts")
	}
}
