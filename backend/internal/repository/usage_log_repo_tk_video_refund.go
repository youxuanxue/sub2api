package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TK: video terminal-failure refund — original-row lookup.
// Implements service.VideoUsageRefundLookupProvider (consumed via type
// assertion, so the wide UsageLogRepository interface and its test stubs
// stay untouched).

// GetVideoUsageByBillingRequestID returns the newest billing_mode='video'
// usage row for (request_id, user_id), or (nil, nil) when absent. The
// billing_mode filter keeps the refund anchored to the submit-time video
// charge even if an unrelated row ever shares the request id.
func (r *usageLogRepository) GetVideoUsageByBillingRequestID(ctx context.Context, requestID string, userID int64) (*service.UsageLog, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage log repository sql db is nil")
	}
	query := fmt.Sprintf(`
		SELECT %s
		FROM usage_logs
		WHERE request_id = $1 AND user_id = $2 AND billing_mode = 'video'
		ORDER BY id DESC
		LIMIT 1
	`, usageLogSelectColumns)
	row := r.db.QueryRowContext(ctx, query, requestID, userID)
	log, err := scanUsageLog(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return log, nil
}
