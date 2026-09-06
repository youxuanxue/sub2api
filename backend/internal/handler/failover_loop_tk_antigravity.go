package handler

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// applyAntigravitySwitchDelay applies the Antigravity platform linear switch delay.
// Non-Antigravity platforms are a no-op (FailoverContinue).
func (s *FailoverState) applyAntigravitySwitchDelay(ctx context.Context, platform string) FailoverAction {
	if platform != service.PlatformAntigravity {
		return FailoverContinue
	}
	delay := time.Duration(s.SwitchCount-1) * time.Second
	if !s.sleep(ctx, delay) {
		return FailoverCanceled
	}
	return FailoverContinue
}
