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
