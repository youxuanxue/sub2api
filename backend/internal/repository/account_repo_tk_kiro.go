package repository

import (
	"fmt"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

func accountKiroRelayStubPredicate() dbpredicate.Account {
	return dbaccount.And(
		dbaccount.PlatformEQ(service.PlatformAnthropic),
		dbaccount.TypeEQ(service.AccountTypeAPIKey),
		dbpredicate.Account(func(s *entsql.Selector) {
			s.Where(entsql.ExprP(fmt.Sprintf("LOWER(TRIM(%s->>'mirror_platform')) = 'kiro'", s.C(dbaccount.FieldCredentials))))
			s.Where(entsql.ExprP(fmt.Sprintf("(%s->>'base_url') ~ '^https://api-[a-z0-9]+\\.tokenkey\\.dev/?$'", s.C(dbaccount.FieldCredentials))))
		}),
	)
}

// tkApplyKiroPlatformListFilter applies Kiro stub / native / Anthropic-excluding-stub
// platform filters for account list queries.
func (r *accountRepository) tkApplyKiroPlatformListFilter(q *dbent.AccountQuery, platform string) *dbent.AccountQuery {
	if platform == service.AccountListPlatformKiroStubFilter {
		return q.Where(accountKiroRelayStubPredicate())
	}
	if platform == service.PlatformKiro {
		return q.Where(
			dbaccount.Or(
				dbaccount.PlatformEQ(service.PlatformKiro),
				accountKiroRelayStubPredicate(),
			),
		)
	}
	if platform == service.PlatformAnthropic {
		return q.Where(
			dbaccount.And(
				dbaccount.PlatformEQ(service.PlatformAnthropic),
				dbaccount.Not(accountKiroRelayStubPredicate()),
			),
		)
	}
	if platform != "" {
		return q.Where(dbaccount.PlatformEQ(platform))
	}
	return q
}
