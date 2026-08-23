package service

import (
	"fmt"
	"strings"
	"time"

	gocache "github.com/patrickmn/go-cache"
)

// tkOpenAICompatSelectionFailureLogDedup suppresses repeated
// openai_account_selection_failed diagnostics to ops_system_logs when the same
// group×platform×model×failure-signature keeps hammering (us6 2026-08-03 storm).
// Per-replica, best-effort — same shape as tkGroupUnsupportedModelNegativeCache.
const (
	tkOpenAICompatSelectionFailureLogDedupTTL     = 60 * time.Second
	tkOpenAICompatSelectionFailureLogDedupCleanup = time.Minute
)

type tkOpenAICompatSelectionFailureLogDedup struct {
	c *gocache.Cache
}

func newTkOpenAICompatSelectionFailureLogDedup() *tkOpenAICompatSelectionFailureLogDedup {
	return newTkOpenAICompatSelectionFailureLogDedupWithTTL(
		tkOpenAICompatSelectionFailureLogDedupTTL,
		tkOpenAICompatSelectionFailureLogDedupCleanup,
	)
}

func newTkOpenAICompatSelectionFailureLogDedupWithTTL(ttl, cleanup time.Duration) *tkOpenAICompatSelectionFailureLogDedup {
	return &tkOpenAICompatSelectionFailureLogDedup{c: gocache.New(ttl, cleanup)}
}

func tkOpenAICompatSelectionFailureLogKey(groupID int64, platform, model string, stats selectionFailureStats) string {
	if groupID <= 0 {
		return ""
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		platform = PlatformOpenAI
	}
	model = strings.ToLower(CanonicalizeOpenAICompatRoutingModel(model))
	if model == "" {
		return ""
	}
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s", groupID, platform, model, selectionFailureStatsFingerprint(stats))
}

func selectionFailureStatsFingerprint(stats selectionFailureStats) string {
	return fmt.Sprintf(
		"t%d:e%d:x%d:u%d:rb%d:mu%d:mrl%d:pt%d:pir%d",
		stats.Total,
		stats.Eligible,
		stats.Excluded,
		stats.Unschedulable,
		stats.RuntimeBlocked,
		stats.ModelUnsupported,
		stats.ModelRateLimited,
		stats.ProfitThreshold,
		stats.ProfitInvalidRate,
	)
}

func (d *tkOpenAICompatSelectionFailureLogDedup) shouldLog(groupID int64, platform, model string, stats selectionFailureStats) bool {
	if d == nil || d.c == nil {
		return true
	}
	key := tkOpenAICompatSelectionFailureLogKey(groupID, platform, model, stats)
	if key == "" {
		return true
	}
	if _, seen := d.c.Get(key); seen {
		return false
	}
	d.c.Set(key, struct{}{}, gocache.DefaultExpiration)
	return true
}
