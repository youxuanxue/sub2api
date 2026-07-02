package service

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gocache "github.com/patrickmn/go-cache"
)

// TK: per-replica negative cache for deterministic "this model is not servable in
// this group" verdicts (ErrUnsupportedModel at account selection). Short-circuits
// repeated hammering through listSchedulable / load-balance without touching upstream.
// Complements tkModelNotFoundNegativeCache (Anthropic upstream 404 after Forward).
// See docs/spec-delta-group-unsupported-model-negative-cache.md.

const (
	tkGroupUnsupportedModelNegativeCacheTTL     = 60 * time.Second
	tkGroupUnsupportedModelNegativeCacheCleanup = time.Minute
)

type tkGroupUnsupportedModelNegativeCache struct {
	c *gocache.Cache
}

func newTkGroupUnsupportedModelNegativeCache() *tkGroupUnsupportedModelNegativeCache {
	return newTkGroupUnsupportedModelNegativeCacheWithTTL(tkGroupUnsupportedModelNegativeCacheTTL, tkGroupUnsupportedModelNegativeCacheCleanup)
}

func newTkGroupUnsupportedModelNegativeCacheWithTTL(ttl, cleanup time.Duration) *tkGroupUnsupportedModelNegativeCache {
	return &tkGroupUnsupportedModelNegativeCache{c: gocache.New(ttl, cleanup)}
}

func tkGroupUnsupportedModelCacheKey(groupID int64, model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || groupID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d\x00%s", groupID, model)
}

func (cstore *tkGroupUnsupportedModelNegativeCache) get(groupID int64, model string) bool {
	if cstore == nil || cstore.c == nil {
		return false
	}
	key := tkGroupUnsupportedModelCacheKey(groupID, model)
	if key == "" {
		return false
	}
	_, ok := cstore.c.Get(key)
	return ok
}

func (cstore *tkGroupUnsupportedModelNegativeCache) put(groupID int64, model string) {
	if cstore == nil || cstore.c == nil {
		return
	}
	key := tkGroupUnsupportedModelCacheKey(groupID, model)
	if key == "" {
		return
	}
	cstore.c.Set(key, struct{}{}, gocache.DefaultExpiration)
	slog.Info("tk_group_unsupported_negative_cache_populate",
		"group_id", groupID,
		"model", strings.ToLower(strings.TrimSpace(model)),
		"ttl", tkGroupUnsupportedModelNegativeCacheTTL.String())
}

func (cstore *tkGroupUnsupportedModelNegativeCache) flush() {
	if cstore == nil || cstore.c == nil {
		return
	}
	cstore.c.Flush()
}

// registerTkGroupUnsupportedModelCacheFlusher wires cache.flush to
// ChannelService.SetGroupUnsupportedModelCacheFlusher so channel CRUD
// invalidateCache drops stale group×model verdicts.
func registerTkGroupUnsupportedModelCacheFlusher(ch *ChannelService, cache *tkGroupUnsupportedModelNegativeCache) {
	if ch == nil || cache == nil {
		return
	}
	ch.SetGroupUnsupportedModelCacheFlusher(cache.flush)
}

func tkGroupUnsupportedModelShortCircuit(cache *tkGroupUnsupportedModelNegativeCache, groupID *int64, model string) error {
	if cache == nil || groupID == nil || *groupID <= 0 {
		return nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if !cache.get(*groupID, model) {
		return nil
	}
	slog.Info("tk_group_unsupported_negative_cache_hit",
		"group_id", *groupID,
		"model", strings.ToLower(model))
	return fmt.Errorf("%w: %s (negative-cache)", ErrUnsupportedModel, model)
}

func tkGroupUnsupportedModelRecordErr(cache *tkGroupUnsupportedModelNegativeCache, groupID *int64, model string, err error) error {
	if err == nil || cache == nil || groupID == nil || *groupID <= 0 {
		return err
	}
	if !errors.Is(err, ErrUnsupportedModel) {
		return err
	}
	cache.put(*groupID, model)
	return err
}

func (s *GatewayService) SetTkGroupUnsupportedModelCache(cache *tkGroupUnsupportedModelNegativeCache) {
	if s == nil {
		return
	}
	s.tkGroupUnsupportedCache = cache
}

func (s *GatewayService) tkGroupUnsupportedModelShortCircuit(groupID *int64, model string) error {
	return tkGroupUnsupportedModelShortCircuit(s.tkGroupUnsupportedCache, groupID, model)
}

func (s *GatewayService) tkGroupUnsupportedModelRecordErr(groupID *int64, model string, err error) error {
	return tkGroupUnsupportedModelRecordErr(s.tkGroupUnsupportedCache, groupID, model, err)
}

func (s *OpenAIGatewayService) SetTkGroupUnsupportedModelCache(cache *tkGroupUnsupportedModelNegativeCache) {
	if s == nil {
		return
	}
	s.tkGroupUnsupportedCache = cache
}

func (s *OpenAIGatewayService) tkGroupUnsupportedModelShortCircuit(groupID *int64, model string) error {
	return tkGroupUnsupportedModelShortCircuit(s.tkGroupUnsupportedCache, groupID, model)
}

func (s *OpenAIGatewayService) tkGroupUnsupportedModelRecordErr(groupID *int64, model string, err error) error {
	return tkGroupUnsupportedModelRecordErr(s.tkGroupUnsupportedCache, groupID, model, err)
}
