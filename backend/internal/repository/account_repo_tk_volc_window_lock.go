package repository

import (
	"fmt"
	"time"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

// accountNotBlockedByAccountWideRateLimit keeps the normal rate-limit filter
// and also admits NewAPI accounts whose account-wide reset was written from a
// per-model 5h/weekly/monthly window. Must stay aligned with
// service.NewAPIAccountWindowLockIgnored.
func accountNotBlockedByAccountWideRateLimit(now time.Time) dbpredicate.Account {
	return dbpredicate.Account(func(s *entsql.Selector) {
		resetCol := s.C(dbaccount.FieldRateLimitResetAt)
		s.Where(entsql.Or(
			entsql.IsNull(resetCol),
			entsql.LTE(resetCol, now),
			newAPIFalseWindowLockPredicate(s),
		))
	})
}

func newAPIFalseWindowLockPredicate(s *entsql.Selector) *entsql.Predicate {
	platform := s.C(dbaccount.FieldPlatform)
	extra := s.C(dbaccount.FieldExtra)
	reset := s.C(dbaccount.FieldRateLimitResetAt)
	match := func(key string) string {
		return fmt.Sprintf(`(%[1]s->>'%[2]s' ~ '^[0-9]+(\.[0-9]+)?$' AND abs((%[1]s->>'%[2]s')::double precision - EXTRACT(EPOCH FROM %[3]s)) < 2)`,
			extra, key, reset)
	}
	intentional := fmt.Sprintf(`(%[1]s->>'%[2]s' ~ '^[0-9]+(\.[0-9]+)?$' AND (%[1]s->>'%[2]s')::double precision > 0)`,
		extra, "newapi_account_window_lock")
	expr := fmt.Sprintf(`(%s = '%s' AND NOT %s AND (%s OR %s OR %s OR %s))`,
		platform, service.PlatformNewAPI,
		intentional,
		match("newapi_weekly_reset"), match("newapi_5h_reset"), match("newapi_7d_reset"), match("newapi_month_reset"),
	)
	return entsql.ExprP(expr)
}
