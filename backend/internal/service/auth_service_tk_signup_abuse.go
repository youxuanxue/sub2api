package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: per-IP signup-bonus withhold. Allows registration to continue while
// capping free balance grants from a single client IP (CallModel C-end open).
//
// Wiring:
//   - SetSignupBonusIPCounter ← ProvideTKAuthServiceColdStart
//   - WithSignupClientIP      ← auth route middleware (routes/auth_tk_antifraud.go)
//   - applySignupBonusUSD consults shouldWithholdSignupBonusForIP
//
// Redis lives in repository (depguard: service must not import go-redis).

type signupClientIPContextKey struct{}

// SignupBonusIPCounter counts signup-bonus grants per client IP per UTC day.
// Implementations MUST be safe for concurrent registration paths.
type SignupBonusIPCounter interface {
	// IncrDaily increments and returns the new count for ip in the current UTC day.
	IncrDaily(ctx context.Context, ip string) (count int64, err error)
}

// WithSignupClientIP attaches the security client IP so register / OAuth create
// paths can enforce the daily bonus cap without widening AuthService signatures.
func WithSignupClientIP(ctx context.Context, clientIP string) context.Context {
	ip := strings.TrimSpace(clientIP)
	if ctx == nil || ip == "" {
		return ctx
	}
	return context.WithValue(ctx, signupClientIPContextKey{}, ip)
}

// SignupClientIPFromContext returns the IP previously attached by WithSignupClientIP.
func SignupClientIPFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ip, _ := ctx.Value(signupClientIPContextKey{}).(string)
	return strings.TrimSpace(ip)
}

// SetSignupBonusIPCounter wires the per-IP bonus counter post-construction.
// Nil-safe; when unset, bonus withhold is skipped (fail-open toward granting).
func (s *AuthService) SetSignupBonusIPCounter(counter SignupBonusIPCounter) {
	if s == nil {
		return
	}
	s.signupBonusIPCounter = counter
}

// shouldWithholdSignupBonusForIP returns true when this IP has already received
// the configured daily bonus quota. Counter errors and missing IP fail-open
// (grant bonus) so registration is never blocked by the antifraud counter.
//
// Side effect: on a grant decision, increments the daily counter so subsequent
// registrations from the same IP see the consumed slot.
func (s *AuthService) shouldWithholdSignupBonusForIP(ctx context.Context, clientIP string) bool {
	if s == nil || s.settingService == nil {
		return false
	}
	ip := strings.TrimSpace(clientIP)
	if ip == "" {
		return false
	}
	limit := s.settingService.GetSignupBonusIPDailyLimit(ctx)
	if limit <= 0 {
		return false
	}
	if s.signupBonusIPCounter == nil {
		return false
	}

	count, err := s.signupBonusIPCounter.IncrDaily(ctx, ip)
	if err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] signup_bonus_ip_counter error (fail-open grant): ip=%s err=%v", ip, err)
		return false
	}
	if count > int64(limit) {
		logger.LegacyPrintf(
			"service.auth",
			"[Auth] signup_bonus_withheld reason=ip_daily_limit ip=%s count=%d limit=%d",
			ip, count, limit,
		)
		return true
	}
	return false
}
