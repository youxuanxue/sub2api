package repository

import (
	"fmt"
	"strings"
	"time"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

// accountNotBlockedByAccountWideRateLimit keeps the normal rate-limit filter
// and also admits NewAPI accounts whose account-wide reset was written from a
// per-model 5h/weekly/monthly window. Must stay aligned with
// service.AccountWideRateLimitResetAt (column lock + model-count cascade).
func accountNotBlockedByAccountWideRateLimit(now time.Time) dbpredicate.Account {
	return dbpredicate.Account(func(s *entsql.Selector) {
		resetCol := s.C(dbaccount.FieldRateLimitResetAt)
		extraCol := s.C(dbaccount.FieldExtra)
		s.Where(entsql.And(
			entsql.Or(
				entsql.IsNull(resetCol),
				entsql.LTE(resetCol, now),
				newAPIFalseWindowLockPredicate(s),
			),
			entsql.ExprP(activeModelRateLimitCountNotOverflowingSQL(extraCol, "NOW()", now)),
		))
	})
}

func newAPIFalseWindowLockPredicate(s *entsql.Selector) *entsql.Predicate {
	platform := s.C(dbaccount.FieldPlatform)
	extra := s.C(dbaccount.FieldExtra)
	reset := s.C(dbaccount.FieldRateLimitResetAt)
	// CASE keeps malformed/overflowing JSON values out of casts. Missing values
	// become zero, matching parseExtraFloat64 instead of propagating SQL NULL.
	number := func(key string) string {
		raw := fmt.Sprintf("btrim(%s->>'%s')", extra, key)
		return fmt.Sprintf("(CASE WHEN pg_input_is_valid(%[1]s, 'double precision') AND lower(%[1]s) <> 'nan' THEN (%[1]s)::double precision ELSE 0 END)", raw)
	}
	matches := make([]string, 0)
	for _, key := range service.NewAPIUsageWindowResetExtraKeys() {
		value := number(key)
		matches = append(matches, fmt.Sprintf("(%s > 0 AND abs(%s - floor(EXTRACT(EPOCH FROM %s))) < 2)", value, value, reset))
	}
	expr := fmt.Sprintf("(%s = '%s' AND %s <= 0 AND (%s))",
		platform, service.PlatformNewAPI, number(service.NewAPIAccountWindowLockExtraKey), strings.Join(matches, " OR "))
	return entsql.ExprP(expr)
}

// activeModelRateLimitCountNotOverflowingSQL admits rows whose counted active
// model_rate_limits scopes are at most AccountWideModelRateLimitThreshold.
// AICredits is excluded to match service.countedActiveModelRateLimits.
//
// nowPlaceholder is replaced with a timestamptz literal when now is non-zero;
// pass "NOW()" and time.Time{} to bind wall clock in raw SQL fragments.
func activeModelRateLimitCountNotOverflowingSQL(extraCol, nowPlaceholder string, now time.Time) string {
	nowExpr := nowPlaceholder
	if !now.IsZero() {
		nowExpr = "'" + now.UTC().Format(time.RFC3339Nano) + "'::timestamptz"
	}
	return fmt.Sprintf(`(
		SELECT COUNT(*)::int
		FROM jsonb_each(COALESCE(%s->'model_rate_limits', '{}'::jsonb)) AS m(key, value)
		WHERE m.key <> 'AICredits'
			AND NULLIF(btrim(m.value->>'rate_limit_reset_at'), '') IS NOT NULL
			AND pg_input_is_valid(m.value->>'rate_limit_reset_at', 'timestamptz')
			AND (m.value->>'rate_limit_reset_at')::timestamptz > %s
	) <= %d`, extraCol, nowExpr, service.AccountWideModelRateLimitThreshold)
}

// accountWideRateLimitNotBlockingSQL is the raw-SQL twin of
// accountNotBlockedByAccountWideRateLimit for queries that cannot use Ent
// predicates. Uses NOW() so it stays consistent with sibling time filters.
func accountWideRateLimitNotBlockingSQL(accountAlias string) string {
	extra := accountAlias + ".extra"
	reset := accountAlias + ".rate_limit_reset_at"
	platform := accountAlias + ".platform"
	number := func(key string) string {
		raw := fmt.Sprintf("btrim(%s->>'%s')", extra, key)
		return fmt.Sprintf("(CASE WHEN pg_input_is_valid(%[1]s, 'double precision') AND lower(%[1]s) <> 'nan' THEN (%[1]s)::double precision ELSE 0 END)", raw)
	}
	matches := make([]string, 0)
	for _, key := range service.NewAPIUsageWindowResetExtraKeys() {
		value := number(key)
		matches = append(matches, fmt.Sprintf("(%s > 0 AND abs(%s - floor(EXTRACT(EPOCH FROM %s))) < 2)", value, value, reset))
	}
	falseLock := fmt.Sprintf("(%s = '%s' AND %s <= 0 AND (%s))",
		platform, service.PlatformNewAPI, number(service.NewAPIAccountWindowLockExtraKey), strings.Join(matches, " OR "))
	return fmt.Sprintf(`(
		(%s IS NULL OR %s <= NOW() OR %s)
		AND %s
	)`, reset, reset, falseLock, activeModelRateLimitCountNotOverflowingSQL(extra, "NOW()", time.Time{}))
}
