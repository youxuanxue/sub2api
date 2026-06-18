package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/engine"
)

func TestAccountRepository_SetTempUnschedulable_NoRowsAffectedDoesNotWriteOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil, nil)
	until := time.Now().Add(10 * time.Minute)

	err := repo.SetTempUnschedulable(context.Background(), 42, until, "retry")
	require.NoError(t, err)
	require.Len(t, exec.execQueries, 1)
	require.Contains(t, exec.execQueries[0], "UPDATE accounts")
	require.NotContains(t, strings.Join(exec.execQueries, "\n"), "scheduler_outbox")
}

func TestAccountRepository_ListOAuthRefreshCandidates_SQLFilter(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var capturedSQL string
	var capturedArgs []any
	mock.ExpectQuery("SELECT id").
		WillReturnRows(sqlmock.NewRows([]string{"id"})).
		WillDelayFor(0)

	repo := newAccountRepositoryWithSQL(nil, captureQuerySQL{db: db, captured: &capturedSQL, capturedArgs: &capturedArgs}, nil, nil)

	accounts, err := repo.ListOAuthRefreshCandidates(context.Background())
	require.NoError(t, err)
	require.Empty(t, accounts)

	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "deleted_at IS NULL")
	require.Contains(t, normalized, "status = 'active'")
	require.Contains(t, normalized, "type = 'oauth'")
	// The platform filter is parametrized (`= ANY($1)`) and bound from the single
	// source of truth engine.OAuthRefreshPlatforms() — there must be NO platform
	// literal left in the SQL for an upstream merge to reset to its four-platform
	// default (the R-001 silent-drop class). The bound arg must carry exactly the
	// source-of-truth list, including TK platforms 6/7 (kiro/grok), which have no
	// on-demand refresh path.
	require.Contains(t, normalized, "platform = ANY($1)",
		"platform filter must be parametrized from engine.OAuthRefreshPlatforms(), not a SQL literal")
	require.NotContains(t, normalized, "platform IN (",
		"no hand-maintained platform IN (...) literal may remain — it is the exact surface upstream silently reset in R-001")
	require.Len(t, capturedArgs, 1, "ListOAuthRefreshCandidates must bind exactly the platform array arg")
	boundPlatforms := pqArrayToStrings(t, capturedArgs[0])
	require.ElementsMatch(t, engine.OAuthRefreshPlatforms(), boundPlatforms,
		"the bound $1 platform array must equal engine.OAuthRefreshPlatforms() verbatim")
	require.Subset(t, boundPlatforms, []string{"kiro", "grok"},
		"TK platforms 6/7 (kiro/grok) must remain OAuth-refresh candidates")
	require.Contains(t, normalized, "credentials ? 'refresh_token'")
	require.Contains(t, normalized, "btrim(credentials->>'refresh_token') <> ''")
	require.Contains(t, normalized, "temp_unschedulable_until > NOW()")
	require.Contains(t, normalized, "temp_unschedulable_reason LIKE 'token refresh retry exhausted:%'")
	require.Contains(t, normalized, "IS NOT TRUE",
		"must use IS NOT TRUE so accounts with NULL temp_unschedulable_until are not silently excluded by PG 3-valued logic")
	require.NotContains(t, normalized, "AND NOT (",
		"plain NOT (...) excludes NULL temp_unschedulable_until rows (the common healthy case)")
	require.Contains(t, normalized, "ORDER BY priority ASC, id ASC")
	require.NotContains(t, normalized, "credentials->>'expires_at'")
	require.NoError(t, mock.ExpectationsWereMet())
}

type captureQuerySQL struct {
	db           *sql.DB
	captured     *string
	capturedArgs *[]any
}

func (c captureQuerySQL) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

func (c captureQuerySQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.captured != nil {
		*c.captured = query
	}
	if c.capturedArgs != nil {
		*c.capturedArgs = args
	}
	return c.db.QueryContext(ctx, query, args...)
}

// pqArrayToStrings unwraps the pq.Array(...) value bound as a SQL arg back into
// the underlying []string so the test can assert it equals the source of truth.
func pqArrayToStrings(t *testing.T, arg any) []string {
	t.Helper()
	sa, ok := arg.(interface{ Value() (driver.Value, error) })
	require.True(t, ok, "bound arg is not a pq.Array driver.Valuer: %T", arg)
	v, err := sa.Value()
	require.NoError(t, err)
	// pq.StringArray serializes to a Postgres array literal like {a,b,c}.
	lit, ok := v.(string)
	require.True(t, ok, "pq.Array value did not serialize to string: %T", v)
	lit = strings.TrimSuffix(strings.TrimPrefix(lit, "{"), "}")
	if lit == "" {
		return nil
	}
	parts := strings.Split(lit, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.Trim(p, `"`))
	}
	return out
}

func normalizeSQLWhitespace(sql string) string {
	return strings.Join(regexp.MustCompile(`\s+`).Split(strings.TrimSpace(sql), -1), " ")
}

type rowsAffectedResult int64

func (r rowsAffectedResult) LastInsertId() (int64, error) { return 0, nil }
func (r rowsAffectedResult) RowsAffected() (int64, error) { return int64(r), nil }

type recordingSQLExecutor struct {
	result      sql.Result
	err         error
	execQueries []string
}

func (e *recordingSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.execQueries = append(e.execQueries, query)
	if e.err != nil {
		return nil, e.err
	}
	return e.result, nil
}

func (e *recordingSQLExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}
