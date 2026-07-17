package repository

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestInsertSystemMetricsUsesCurrentColumnPlaceholderCount(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(
		func(expectedSQL, actualSQL string) error {
			if expectedSQL != "ops_system_metrics_insert" {
				return fmt.Errorf("unexpected expected sql marker %q", expectedSQL)
			}
			columns, values, err := countInsertColumnsAndValues(actualSQL, "ops_system_metrics")
			if err != nil {
				return err
			}
			if columns != 39 || values != 39 {
				return fmt.Errorf("ops_system_metrics insert has %d columns and %d values, want 39/39", columns, values)
			}
			if strings.Contains(actualSQL, "$40") {
				return fmt.Errorf("ops_system_metrics insert still references removed $40 placeholder")
			}
			return nil
		},
	)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := &opsRepository{db: db}

	input := &service.OpsInsertSystemMetricsInput{
		CreatedAt:     time.Date(2026, 7, 9, 14, 30, 0, 0, time.UTC),
		WindowMinutes: 1,
		SuccessCount:  12,
	}

	mock.ExpectExec("ops_system_metrics_insert").
		WithArgs(anySQLArgs(39)...).
		WillReturnResult(sqlmock.NewResult(1, 1))

	require.NoError(t, repo.InsertSystemMetrics(context.Background(), input))
	require.NoError(t, mock.ExpectationsWereMet())
}

func countInsertColumnsAndValues(query, table string) (int, int, error) {
	intro := "INSERT INTO " + table
	introIdx := strings.Index(query, intro)
	if introIdx < 0 {
		return 0, 0, fmt.Errorf("missing %q", intro)
	}
	columnStartRel := strings.Index(query[introIdx+len(intro):], "(")
	if columnStartRel < 0 {
		return 0, 0, fmt.Errorf("missing column list")
	}
	columnStart := introIdx + len(intro) + columnStartRel
	columnEnd, err := matchingParen(query, columnStart)
	if err != nil {
		return 0, 0, err
	}

	valuesIdxRel := strings.Index(strings.ToUpper(query[columnEnd:]), "VALUES")
	if valuesIdxRel < 0 {
		return 0, 0, fmt.Errorf("missing VALUES")
	}
	valuesIdx := columnEnd + valuesIdxRel
	valuesStartRel := strings.Index(query[valuesIdx:], "(")
	if valuesStartRel < 0 {
		return 0, 0, fmt.Errorf("missing values list")
	}
	valuesStart := valuesIdx + valuesStartRel
	valuesEnd, err := matchingParen(query, valuesStart)
	if err != nil {
		return 0, 0, err
	}

	return countCSVItems(query[columnStart+1 : columnEnd]), countCSVItems(query[valuesStart+1 : valuesEnd]), nil
}

func matchingParen(s string, open int) (int, error) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unclosed parenthesis")
}

func countCSVItems(s string) int {
	count := 0
	for _, part := range strings.Split(s, ",") {
		if strings.TrimSpace(part) != "" {
			count++
		}
	}
	return count
}

func anySQLArgs(n int) []driver.Value {
	args := make([]driver.Value, n)
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	return args
}
