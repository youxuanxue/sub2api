package repository

import (
	"fmt"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

// accountNotBlockedByAccountWideRateLimit keeps the normal rate-limit filter
// and also admits VolcEngine Agent Plan accounts whose account-wide reset was
// written from a per-model usage window. Must stay aligned with
// service.VolcAgentPlanAccountWindowLockIgnored.
func accountNotBlockedByAccountWideRateLimit(now time.Time) dbpredicate.Account {
	return dbpredicate.Account(func(s *entsql.Selector) {
		resetCol := s.C(dbaccount.FieldRateLimitResetAt)
		s.Where(entsql.Or(
			entsql.IsNull(resetCol),
			entsql.LTE(resetCol, now),
			volcAgentPlanFalseWindowLockPredicate(s),
		))
	})
}

func volcAgentPlanFalseWindowLockPredicate(s *entsql.Selector) *entsql.Predicate {
	platform := s.C(dbaccount.FieldPlatform)
	channel := s.C(dbaccount.FieldChannelType)
	creds := s.C(dbaccount.FieldCredentials)
	extra := s.C(dbaccount.FieldExtra)
	reset := s.C(dbaccount.FieldRateLimitResetAt)
	match := func(key string) string {
		return fmt.Sprintf(`(%[1]s->>'%[2]s' ~ '^[0-9]+(\.[0-9]+)?$' AND abs((%[1]s->>'%[2]s')::double precision - EXTRACT(EPOCH FROM %[3]s)) < 2)`,
			extra, key, reset)
	}
	expr := fmt.Sprintf(`(%s = '%s' AND %s = %d AND ((%s->>'base_url') IN ('%s', '%s') OR (%s->>'base_url') LIKE '%%/api/plan/v3%%' OR (%s->>'base_url') LIKE '%%/api/plan') AND (%s OR %s OR %s))`,
		platform, service.PlatformNewAPI,
		channel, newapiconstant.ChannelTypeVolcEngine,
		creds, newapiintegration.VolcEngineAgentPlanBaseKey, newapiintegration.VolcEngineAgentPlanBaseURL,
		creds, creds,
		match("newapi_weekly_reset"), match("newapi_5h_reset"), match("newapi_7d_reset"),
	)
	return entsql.ExprP(expr)
}
