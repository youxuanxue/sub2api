package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryListRequestDetails_LatencySort(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sort  string
		order string
	}{
		{name: "TTFT", sort: "ttft_desc", order: "first_token_ms DESC NULLS LAST, created_at DESC"},
		{name: "duration", sort: "duration_desc", order: "duration_ms DESC NULLS LAST, created_at DESC"},
		{name: "default", order: "created_at DESC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &opsRepository{db: db}
			start := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
			end := start.Add(time.Hour)
			filter := &service.OpsRequestDetailFilter{
				StartTime: &start,
				EndTime:   &end,
				Sort:      tc.sort,
				Page:      2,
				PageSize:  10,
			}

			mock.ExpectQuery(`SELECT COUNT\(1\) FROM combined`).
				WithArgs(start, end).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(13))
			rows := sqlmock.NewRows([]string{
				"kind", "created_at", "request_id", "platform", "model", "duration_ms", "first_token_ms",
				"status_code", "error_id", "phase", "severity", "message", "user_id", "api_key_id", "account_id", "group_id",
				"user_email", "username", "api_key_name", "group_name", "account_name",
				"stream",
			}).
				AddRow("success", start, "req-slow", "openai", "gpt-5.5", 12000, 800, nil, nil, nil, nil, nil, 1, 2, 3, 4, "u@example.com", "user1", "key1", "grp1", "acc1", true).
				AddRow("error", start, "req-zero", "openai", "gpt-5.5", 9000, 0, 502, 5, "upstream", "error", "failed", 1, 2, 3, 4, "u@example.com", "user1", "key1", "grp1", "acc1", true).
				AddRow("success", start, "req-missing", "openai", "gpt-5.5", 5000, nil, nil, nil, nil, nil, nil, 1, 2, 3, 4, "u@example.com", "user1", "key1", "grp1", "acc1", false)
			mock.ExpectQuery(`(?s)ul\.first_token_ms AS first_token_ms.*o\.time_to_first_token_ms AS first_token_ms.*SELECT.*duration_ms,\s+first_token_ms,.*ORDER BY `+tc.order+`\s+LIMIT \$3 OFFSET \$4`).
				WithArgs(start, end, 10, 10).
				WillReturnRows(rows)

			items, total, err := repo.ListRequestDetails(context.Background(), filter)
			require.NoError(t, err)
			require.EqualValues(t, 13, total)
			require.Len(t, items, 3)
			require.NotNil(t, items[0].FirstTokenMs)
			require.Equal(t, 800, *items[0].FirstTokenMs)
			require.Equal(t, 12000, *items[0].DurationMs)
			require.NotNil(t, items[1].FirstTokenMs)
			require.Zero(t, *items[1].FirstTokenMs)
			require.Nil(t, items[2].FirstTokenMs)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestOpsRequestDetailsErrorSideDurationHasAWriter pins where the error side of the
// UNION gets its duration. ops_error_logs.duration_ms was declared but never written
// (always NULL) and was dropped in tk_100, so this side must read the written
// response_latency_ms instead — otherwise duration is blank for every error row and a
// MinDurationMs filter silently excludes all of them. Asserted against the source so
// the query need not be refactored into a constant just to be testable.
func TestOpsRequestDetailsErrorSideDurationHasAWriter(t *testing.T) {
	src, err := os.ReadFile("ops_repo_request_details.go")
	require.NoError(t, err)
	sql := string(src)

	require.Contains(t, sql, "o.response_latency_ms AS duration_ms",
		"error side must project a written latency column as duration_ms")
	require.NotContains(t, sql, "o.duration_ms AS duration_ms",
		"ops_error_logs.duration_ms had no writer and was dropped in tk_100")
	require.Contains(t, sql, "ul.duration_ms AS duration_ms",
		"the usage side keeps its own real column")
}
