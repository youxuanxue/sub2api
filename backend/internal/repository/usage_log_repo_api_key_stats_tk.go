package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// GetAPIKeyStatsWithUsageFilters returns per-API-key usage statistics for the given filters.
// Results are ordered by total tokens descending (same ranking default as model/group distribution).
func (r *usageLogRepository) GetAPIKeyStatsWithUsageFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	filters UsageLogFilters,
) (results []usagestats.APIKeyStat, err error) {
	query := `
		SELECT
			COALESCE(ul.api_key_id, 0) as api_key_id,
			COALESCE(k.name, '') as api_key_name,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens), 0) as input_tokens,
			COALESCE(SUM(ul.output_tokens), 0) as output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(ul.cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost
		FROM usage_logs ul
		LEFT JOIN api_keys k ON k.id = ul.api_key_id
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`

	args := []any{startTime, endTime}
	if filters.UserID > 0 {
		query += fmt.Sprintf(" AND ul.user_id = $%d", len(args)+1)
		args = append(args, filters.UserID)
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, filters.RequestType, filters.Stream)
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query += `
		GROUP BY ul.api_key_id, k.name
		ORDER BY (
			COALESCE(SUM(ul.input_tokens), 0)
			+ COALESCE(SUM(ul.output_tokens), 0)
			+ COALESCE(SUM(ul.cache_creation_tokens), 0)
			+ COALESCE(SUM(ul.cache_read_tokens), 0)
		) DESC
	`

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.APIKeyStat, 0)
	for rows.Next() {
		var row usagestats.APIKeyStat
		if err = rows.Scan(
			&row.APIKeyID,
			&row.APIKeyName,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheCreationTokens,
			&row.CacheReadTokens,
			&row.Cost,
			&row.ActualCost,
		); err != nil {
			return nil, err
		}
		row.TotalTokens = row.InputTokens + row.OutputTokens + row.CacheCreationTokens + row.CacheReadTokens
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
