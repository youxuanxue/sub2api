package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ClearObservedUsageWindows deletes provider-observed usage/window Extra keys so
// admin recover-state can wait for fresh request evidence. Key list SSOT:
// service.ObservedUsageWindowExtraKeys.
func (r *accountRepository) ClearObservedUsageWindows(ctx context.Context, id int64) error {
	keys := service.ObservedUsageWindowExtraKeys()
	if len(keys) == 0 {
		return nil
	}

	extraExpr := "COALESCE(extra, '{}'::jsonb)"
	for _, key := range keys {
		// Keys are compile-time constants from ObservedUsageWindowExtraKeys.
		extraExpr += fmt.Sprintf(" - '%s'", strings.ReplaceAll(key, "'", "''"))
	}

	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(
		ctx,
		"UPDATE accounts SET extra = "+extraExpr+", updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL",
		id,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue clear observed usage windows failed: account=%d err=%v", id, err)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return nil
}
