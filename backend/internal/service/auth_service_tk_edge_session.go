package service

import (
	"context"
	"strings"
)

func isEdgeHandoffFamily(family string) bool { return strings.HasPrefix(family, "edge-handoff-") }

// GenerateEdgeAdminSessionTokenPair uses the existing refresh owner. Initial and
// rotated refresh tokens in this named family require a revocable index.
func (s *AuthService) GenerateEdgeAdminSessionTokenPair(ctx context.Context, user *User, family string) (*TokenPair, error) {
	if user == nil || !user.IsAdmin() || !user.IsActive() || !isEdgeHandoffFamily(family) {
		return nil, ErrEdgeHandoffInvalid
	}
	return s.GenerateTokenPair(ctx, user, family)
}
