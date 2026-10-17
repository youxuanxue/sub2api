//go:build unit

package service

import "testing"

func TestCandidatePathLessTokenseaPreferenceOverridesGlobalPriority(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	tokensea := &candidateExecutionPath{account: &Account{ID: 136, Priority: 20}}
	other := &candidateExecutionPath{account: &Account{ID: 300, Priority: 1}}

	if !candidatePathLess(key, nil, nil, tokensea, other) {
		t.Fatal("tokensea preference must outrank a lower global priority account")
	}
}

func TestCandidatePathLessDefaultKeepsGlobalPriority(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceDefault}
	preferred := &candidateExecutionPath{account: &Account{ID: 136, Priority: 20}}
	other := &candidateExecutionPath{account: &Account{ID: 300, Priority: 1}}

	if candidatePathLess(key, nil, nil, preferred, other) {
		t.Fatal("default preference must preserve global priority")
	}
}

func TestCandidatePreferenceOnlyMatchesConfiguredTokenseaAccounts(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	if candidatePreferenceRank(key, &Account{ID: 136}) != 0 {
		t.Fatal("account 136 must be preferred")
	}
	if candidatePreferenceRank(key, &Account{ID: 218}) != 0 {
		t.Fatal("account 218 must be preferred")
	}
	if candidatePreferenceRank(key, &Account{ID: 219}) != 1 {
		t.Fatal("unconfigured account must remain in fallback tier")
	}
}
