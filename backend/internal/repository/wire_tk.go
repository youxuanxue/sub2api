package repository

import "github.com/google/wire"

// TKProviderSet is the Wire provider set for TokenKey-only repository DI.
// Composed into ProviderSet so upstream-shaped wire.go stays free of TK
// provider listings (CLAUDE.md §5 companion pattern).
var TKProviderSet = wire.NewSet(
	NewTierRepository,
	NewModelAvailabilityRepository,
	NewRateLimitExpiryRepository,
	NewTierCache,
)
