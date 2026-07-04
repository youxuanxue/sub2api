package service

type accountUsageWindowAdapter string

// SSOT: platform values sourced from domain.Platform* via domain_constants.go.
const (
	accountUsageWindowAdapterUnsupported accountUsageWindowAdapter = ""
	accountUsageWindowAdapterAnthropic   accountUsageWindowAdapter = PlatformAnthropic
	accountUsageWindowAdapterOpenAI      accountUsageWindowAdapter = PlatformOpenAI
	accountUsageWindowAdapterGemini      accountUsageWindowAdapter = PlatformGemini
	accountUsageWindowAdapterAntigravity accountUsageWindowAdapter = PlatformAntigravity
	accountUsageWindowAdapterKiro        accountUsageWindowAdapter = PlatformKiro
	accountUsageWindowAdapterLocal       accountUsageWindowAdapter = "local"
)

// accountUsageWindowAdapterFor is the single dispatch point for account usage
// windows. Platform-specific probes can have different upstream protocols, but
// GetUsage, GetPassiveUsage, and GetPassiveUsageBatch must agree on this owner.
func accountUsageWindowAdapterFor(account *Account) accountUsageWindowAdapter {
	if account == nil {
		return accountUsageWindowAdapterUnsupported
	}
	if account.IsOpenAIOAuth() {
		return accountUsageWindowAdapterOpenAI
	}
	if account.IsKiro() {
		return accountUsageWindowAdapterKiro
	}
	if account.IsGrok() || account.Platform == PlatformNewAPI {
		return accountUsageWindowAdapterLocal
	}

	switch account.Platform {
	case PlatformGemini:
		return accountUsageWindowAdapterGemini
	case PlatformAntigravity:
		if account.Type == AccountTypeOAuth {
			return accountUsageWindowAdapterAntigravity
		}
		return accountUsageWindowAdapterLocal
	case PlatformAnthropic:
		if account.Type == AccountTypeOAuth || account.Type == AccountTypeSetupToken {
			return accountUsageWindowAdapterAnthropic
		}
	}
	return accountUsageWindowAdapterUnsupported
}

func accountUsageWindowAdapterSupportsPassive(adapter accountUsageWindowAdapter) bool {
	switch adapter {
	case accountUsageWindowAdapterAnthropic,
		accountUsageWindowAdapterOpenAI,
		accountUsageWindowAdapterKiro,
		accountUsageWindowAdapterLocal:
		return true
	default:
		return false
	}
}
