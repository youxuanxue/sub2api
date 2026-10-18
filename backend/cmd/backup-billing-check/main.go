// backup-billing-check exercises the real billing repository on a disposable
// restored database. It never starts the application or accepts a network DSN.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
)

func check(ctx context.Context, db *sql.DB) error {
	var isolated bool
	if err := db.QueryRowContext(ctx, `SELECT current_setting('archive_mode')='off'
        AND current_setting('listen_addresses')='' AND NOT pg_is_in_recovery()`).Scan(&isolated); err != nil {
		return err
	}
	if !isolated {
		return errors.New("requires a recovered, socket-only database with archiving disabled")
	}
	var userID, keyID int64
	var quotaEnabled bool
	if err := db.QueryRowContext(ctx, `SELECT u.id,k.id,k.quota>0 FROM users u JOIN api_keys k ON k.user_id=u.id
        WHERE u.deleted_at IS NULL AND k.deleted_at IS NULL ORDER BY u.id,k.id LIMIT 1`).Scan(&userID, &keyID, &quotaEnabled); err != nil {
		return err
	}
	balance := func() (string, error) {
		var value string
		err := db.QueryRowContext(ctx, "SELECT json_build_array(u.balance,k.quota_used)::text FROM users u JOIN api_keys k ON k.id=$2 WHERE u.id=$1", userID, keyID).Scan(&value)
		return value, err
	}
	before, err := balance()
	if err != nil {
		return err
	}
	repo := repository.NewUsageBillingRepository(nil, db)
	request := fmt.Sprintf("physical-restore-check-%d", time.Now().UnixNano())
	cmd := &service.UsageBillingCommand{RequestID: request, UserID: userID, APIKeyID: keyID,
		BillingType: service.BillingTypeBalance, BalanceCost: 0.01}
	if quotaEnabled {
		cmd.APIKeyQuotaCost = 0.01
	}
	result, err := repo.Apply(ctx, cmd)
	if err != nil {
		return err
	}
	if !result.Applied {
		return errors.New("first charge was not applied")
	}
	afterCharge, err := balance()
	if err != nil {
		return err
	}
	if afterCharge == before {
		return errors.New("charge did not change balance")
	}
	for _, archived := range []bool{false, true} {
		if archived {
			// Copy only this synthetic request. The isolated cluster is deleted
			// after acceptance, so no production data cleanup is necessary.
			_, err = db.ExecContext(ctx, `WITH moved AS (DELETE FROM usage_billing_dedup
              WHERE request_id=$1 AND api_key_id=$2 RETURNING request_id,api_key_id,request_fingerprint,created_at)
              INSERT INTO usage_billing_dedup_archive(request_id,api_key_id,request_fingerprint,created_at)
              SELECT request_id,api_key_id,request_fingerprint,created_at FROM moved`, request, keyID)
			if err != nil {
				return err
			}
		}
		result, err = repo.Apply(ctx, cmd)
		if err != nil {
			return err
		}
		current, err := balance()
		if err != nil {
			return err
		}
		if result.Applied || current != afterCharge {
			return errors.New("duplicate charge changed restored balance")
		}
	}
	refund, ok := repo.(service.UsageBillingVideoRefundApplier)
	if !ok {
		return errors.New("billing repository does not implement refund acceptance")
	}
	refundCmd := &service.VideoRefundCommand{RequestID: "video-refund:" + request, UserID: userID,
		APIKeyID: keyID, BillingType: service.BillingTypeBalance, Amount: 0.01}
	for attempt := 0; attempt < 2; attempt++ {
		applied, err := refund.ApplyVideoRefund(ctx, refundCmd)
		if err != nil {
			return err
		}
		current, err := balance()
		if err != nil {
			return err
		}
		if applied != (attempt == 0) || current != before {
			return errors.New("refund retry changed restored balance")
		}
	}
	return nil
}

func main() {
	if os.Getenv("TOKENKEY_DISPOSABLE_RESTORE") != "yes" {
		fmt.Fprintln(os.Stderr, "requires an explicitly disposable restore container")
		os.Exit(1)
	}
	db, err := sql.Open("postgres", "host=/var/run/postgresql user=tokenkey dbname=tokenkey sslmode=disable")
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err = check(ctx, db)
		closeErr := db.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		// DB error text may include private data. Return only failure status.
		fmt.Fprintln(os.Stderr, "restored billing acceptance failed")
		os.Exit(1)
	}
	fmt.Println(`{"status":"ok","charge_retry":true,"archive_retry":true,"refund_retry":true}`)
}
