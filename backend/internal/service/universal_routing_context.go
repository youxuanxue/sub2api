package service

import "context"

type universalKeyRoutingContextKey struct{}

// WithUniversalKeyRouting preserves request identity across billing-group binding.
func WithUniversalKeyRouting(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, universalKeyRoutingContextKey{}, true)
}

func IsUniversalKeyRouting(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	universal, _ := ctx.Value(universalKeyRoutingContextKey{}).(bool)
	return universal
}
