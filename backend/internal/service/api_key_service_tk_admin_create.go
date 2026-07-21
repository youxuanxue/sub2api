package service

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

// CreateAsAdmin creates an API key on behalf of a user, bypassing end-user group
// permission checks while still validating user/group existence and group status.
// Used by admin relay provisioning (prod→edge mirror stubs).
func (s *APIKeyService) CreateAsAdmin(ctx context.Context, userID int64, req CreateAPIKeyRequest) (*APIKey, error) {
	if _, err := s.userRepo.GetByID(ctx, userID); err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	if len(req.IPWhitelist) > 0 {
		if invalid := ip.ValidateIPPatterns(req.IPWhitelist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}
	if len(req.IPBlacklist) > 0 {
		if invalid := ip.ValidateIPPatterns(req.IPBlacklist); len(invalid) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrInvalidIPPattern, invalid)
		}
	}

	routingMode := RoutingModeDirect
	switch {
	case req.RoutingMode != nil:
		routingMode = strings.TrimSpace(*req.RoutingMode)
	case req.GroupID == nil:
		routingMode = RoutingModeUniversal
	}
	if routingMode != RoutingModeDirect && routingMode != RoutingModeUniversal {
		return nil, ErrInvalidRoutingMode
	}

	effectiveGroupID := req.GroupID
	if routingMode == RoutingModeUniversal {
		effectiveGroupID = nil
	}

	if effectiveGroupID != nil {
		group, err := s.groupRepo.GetByID(ctx, *effectiveGroupID)
		if err != nil {
			return nil, fmt.Errorf("get group: %w", err)
		}
		if group.Status != StatusActive {
			return nil, infraerrors.BadRequest("GROUP_NOT_ACTIVE", "target group is not active")
		}
	}

	var key string
	if req.CustomKey != nil && *req.CustomKey != "" {
		if err := s.ValidateCustomKey(*req.CustomKey); err != nil {
			return nil, err
		}
		exists, err := s.apiKeyRepo.ExistsByKey(ctx, *req.CustomKey)
		if err != nil {
			return nil, fmt.Errorf("check key exists: %w", err)
		}
		if exists {
			return nil, ErrAPIKeyExists
		}
		key = *req.CustomKey
	} else {
		var err error
		key, err = s.GenerateKey()
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
	}

	apiKey := &APIKey{
		UserID:      userID,
		Key:         key,
		Name:        html.EscapeString(req.Name),
		GroupID:     effectiveGroupID,
		RoutingMode: routingMode,
		Status:      StatusActive,
		IPWhitelist: req.IPWhitelist,
		IPBlacklist: req.IPBlacklist,
		Quota:       req.Quota,
		QuotaUsed:   0,
		RateLimit5h: req.RateLimit5h,
		RateLimit1d: req.RateLimit1d,
		RateLimit7d: req.RateLimit7d,
	}

	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		expiresAt := time.Now().AddDate(0, 0, *req.ExpiresInDays)
		apiKey.ExpiresAt = &expiresAt
	}

	if err := s.apiKeyRepo.Create(ctx, apiKey); err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}

	s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	s.compileAPIKeyIPRules(apiKey)

	return apiKey, nil
}
