package service

import (
	kiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

func normalizeAccountPriority(platform string, priority int) int {
	if platform == PlatformKiro && priority <= 0 {
		return kiro.DefaultKiroAccountPriority
	}
	return priority
}
