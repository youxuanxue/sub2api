package service

import "time"

// Shared window-utilization soft-scheduling defaults for Codex and Anthropic OAuth
// lines. Single source of truth — tier table does NOT store these.
const (
	windowUtilStickyThresholdDefault = 0.98
	windowUtilStickyReserveDefault   = 0.02
)

// WindowUtilSchedulability is the tri-state result of comparing upstream window
// utilization against threshold + reserve bands.
type WindowUtilSchedulability int

const (
	WindowUtilSchedulable WindowUtilSchedulability = iota
	WindowUtilStickyOnly
	WindowUtilNotSchedulable
)

// checkWindowUtilSchedulability maps utilization in [0,1] to a schedulability state.
//   - util < threshold                     -> Schedulable
//   - threshold <= util < threshold+reserve -> StickyOnly
//   - util >= threshold+reserve             -> NotSchedulable
//
// A non-positive or >=1 threshold disables the restriction (Schedulable).
func checkWindowUtilSchedulability(util, stickyThreshold, stickyReserve float64) WindowUtilSchedulability {
	if stickyThreshold <= 0 || stickyThreshold >= 1 {
		return WindowUtilSchedulable
	}
	if util < stickyThreshold {
		return WindowUtilSchedulable
	}
	if stickyReserve < 0 {
		stickyReserve = 0
	}
	if util < stickyThreshold+stickyReserve {
		return WindowUtilStickyOnly
	}
	return WindowUtilNotSchedulable
}

type windowUtilReader func(account *Account, now time.Time) (util float64, ok bool)

// leastUtilizedByWindowUtil picks the account with the lowest utilization among
// dropped candidates (never-empty-pool fallback). No-signal accounts count as 0.
func leastUtilizedByWindowUtil(dropped []*Account, now time.Time, read windowUtilReader) *Account {
	if len(dropped) == 0 || read == nil {
		return nil
	}
	var best *Account
	bestUtil := 2.0
	for _, acc := range dropped {
		util, ok := read(acc, now)
		if !ok {
			util = 0
		}
		if best == nil || util < bestUtil {
			bestUtil = util
			best = acc
		}
	}
	return best
}

// schedulableForWindowUtil is the shared gate used by Codex and Anthropic adapters.
func schedulableForWindowUtil(util float64, hasSignal bool, threshold, reserve float64, enabled, isSticky bool) bool {
	if !enabled || !hasSignal {
		return true
	}
	switch checkWindowUtilSchedulability(util, threshold, reserve) {
	case WindowUtilStickyOnly:
		return isSticky
	case WindowUtilNotSchedulable:
		return false
	default:
		return true
	}
}
