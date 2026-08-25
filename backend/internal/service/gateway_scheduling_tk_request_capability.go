package service

import "context"

type nativeGeminiVertexAccountRequirementContextKey struct{}

func WithNativeGeminiVertexAccountRequirement(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeGeminiVertexAccountRequirementContextKey{}, true)
}

func nativeGeminiVertexAccountRequired(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	required, _ := ctx.Value(nativeGeminiVertexAccountRequirementContextKey{}).(bool)
	return required
}

func accountSupportsNativeGeminiVertexRequirement(ctx context.Context, account *Account) bool {
	if account == nil {
		return false
	}
	return !nativeGeminiVertexAccountRequired(ctx) || account.IsNewAPIVertexServiceAccount()
}

func filterAccountsForNativeGeminiVertexRequirement(ctx context.Context, accounts []Account) []Account {
	if !nativeGeminiVertexAccountRequired(ctx) {
		return accounts
	}
	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		if accountSupportsNativeGeminiVertexRequirement(ctx, &accounts[i]) {
			filtered = append(filtered, accounts[i])
		}
	}
	return filtered
}
