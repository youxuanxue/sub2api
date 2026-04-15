package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// resolveOpenAICompatPlatform returns the account-listing platform for
// the OpenAI-compat gateway path. NewAPI groups store accounts under
// PlatformNewAPI; all others use PlatformOpenAI.
func resolveOpenAICompatPlatform(ctx context.Context) string {
	if g, ok := ctx.Value(ctxkey.Group).(*Group); ok && g != nil && g.Platform == PlatformNewAPI {
		return PlatformNewAPI
	}
	return PlatformOpenAI
}

// isOpenAICompatAccount returns true for accounts that belong to the
// OpenAI-compatible gateway path: both native OpenAI and NewAPI accounts.
func isOpenAICompatAccount(a *Account) bool {
	return a != nil && (a.Platform == PlatformOpenAI || a.Platform == PlatformNewAPI)
}
