package service

// These production tokensea Claude suppliers are NewAPI accounts, not native
// Anthropic accounts. The preference remains an ordering hint and
// never bypasses the normal candidate eligibility gates.
const (
	tokenseaAnthropicAccount136 int64 = 136
	tokenseaAnthropicAccount218 int64 = 218
)

func candidatePreferenceRank(key *APIKey, account *Account, model string) int {
	if key == nil || account == nil || account.Platform != PlatformNewAPI || !tkIsForwardableAnthropicModelName(model) {
		return 1
	}
	switch key.RoutingPreference {
	case RoutingPreferenceTokenseaFirst:
		if account.ID == tokenseaAnthropicAccount136 || account.ID == tokenseaAnthropicAccount218 {
			return 0
		}
	}
	return 1
}
