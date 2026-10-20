package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

const userGroupRateLoadTimeout = 3 * time.Second

type userGroupRateResolver struct {
	repo         UserGroupRateRepository
	cache        *gocache.Cache
	cacheTTL     time.Duration
	sf           *singleflight.Group
	logComponent string
}

func newUserGroupRateResolver(repo UserGroupRateRepository, cache *gocache.Cache, cacheTTL time.Duration, sf *singleflight.Group, logComponent string) *userGroupRateResolver {
	if cacheTTL <= 0 {
		cacheTTL = defaultUserGroupRateCacheTTL
	}
	if cache == nil {
		cache = gocache.New(cacheTTL, time.Minute)
	}
	if logComponent == "" {
		logComponent = "service.gateway"
	}
	if sf == nil {
		sf = &singleflight.Group{}
	}

	return &userGroupRateResolver{
		repo:         repo,
		cache:        cache,
		cacheTTL:     cacheTTL,
		sf:           sf,
		logComponent: logComponent,
	}
}

func (r *userGroupRateResolver) Resolve(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	multiplier, err := r.ResolveForBilling(ctx, userID, groupID, groupDefaultMultiplier)
	if err != nil {
		// Legacy estimates/admission callers cannot return an error. Settlement
		// must use ResolveForBilling and must never charge this estimate.
		return 1
	}
	return multiplier
}

// ResolveForBilling only returns a multiplier confirmed by cache or repository.
// A failed lookup is not evidence that the user has no negotiated discount.
func (r *userGroupRateResolver) ResolveForBilling(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) (float64, error) {
	if r == nil || userID <= 0 || groupID <= 0 {
		return groupDefaultMultiplier, nil
	}

	key := fmt.Sprintf("%d:%d", userID, groupID)
	if r.cache != nil {
		if cached, ok := r.cache.Get(key); ok {
			if multiplier, castOK := cached.(float64); castOK {
				userGroupRateCacheHitTotal.Add(1)
				return multiplier, nil
			}
		}
	}
	if r.repo == nil {
		return groupDefaultMultiplier, nil
	}
	userGroupRateCacheMissTotal.Add(1)

	value, err, shared := r.sf.Do(key, func() (any, error) {
		if r.cache != nil {
			if cached, ok := r.cache.Get(key); ok {
				if multiplier, castOK := cached.(float64); castOK {
					userGroupRateCacheHitTotal.Add(1)
					return multiplier, nil
				}
			}
		}

		userGroupRateCacheLoadTotal.Add(1)
		// A canceled leader must not cancel the shared read for other callers.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), userGroupRateLoadTimeout)
		defer cancel()
		userRate, repoErr := r.repo.GetByUserAndGroup(loadCtx, userID, groupID)
		if repoErr != nil {
			return nil, repoErr
		}

		multiplier := groupDefaultMultiplier
		if userRate != nil {
			multiplier = *userRate
		}
		if r.cache != nil {
			r.cache.Set(key, multiplier, r.cacheTTL)
		}
		return multiplier, nil
	})
	if shared {
		userGroupRateCacheSFSharedTotal.Add(1)
	}
	if err != nil {
		userGroupRateCacheFallbackTotal.Add(1)
		logger.LegacyPrintf(r.logComponent, "get user group rate failed; billing rate unavailable: user=%d group=%d err=%v", userID, groupID, err)
		return 0, fmt.Errorf("resolve billing rate user=%d group=%d: %w", userID, groupID, err)
	}

	multiplier, ok := value.(float64)
	if !ok {
		userGroupRateCacheFallbackTotal.Add(1)
		return 0, fmt.Errorf("invalid billing rate user=%d group=%d", userID, groupID)
	}
	return multiplier, nil
}
