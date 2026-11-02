//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestUpstream400StreakRepository_OpenStreakRules pins the streak SQL on a real
// Postgres: only the latest run of identical upstream 400s after the last
// success counts; digits/hex ids do not split a signature; a different
// signature or a success breaks the run; span and count gates both apply.
func TestUpstream400StreakRepository_OpenStreakRules(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")
	client := testEntClient(t)
	repo := NewUpstream400StreakRepository(integrationDB)

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("streak-%d@test.com", time.Now().UnixNano())})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: fmt.Sprintf("sk-streak-%d", time.Now().UnixNano()), Name: "streak"})
	mk := func(name string) int64 {
		return mustCreateAccount(t, client, &service.Account{Name: name, Schedulable: true}).ID
	}
	expired, broken, recovered, short, mixed := mk("st-expired"), mk("st-broken"), mk("st-recovered"), mk("st-short"), mk("st-mixed")

	now := time.Now().UTC().Truncate(time.Second)
	insErr := func(acc int64, at time.Time, msg string) {
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (error_phase, error_type, severity, status_code, upstream_status_code,
				error_owner, platform, account_id, user_id, upstream_error_message, created_at)
			VALUES ('request', 'invalid_request_error', 'error', 400, 400, 'client', 'newapi', $1, $2, $3, $4)`,
			acc, user.ID, msg, at)
		require.NoError(t, err)
	}
	insOK := func(acc int64, at time.Time) {
		_, err := client.UsageLog.Create().SetUserID(user.ID).SetAPIKeyID(key.ID).SetAccountID(acc).
			SetRequestID(fmt.Sprintf("ok-%d-%d", acc, at.UnixNano())).SetModel("m").SetCreatedAt(at).Save(ctx)
		require.NoError(t, err)
	}
	expiredMsg := func(i int) string {
		return fmt.Sprintf("Your account (21313999%02d) does not have a valid AgentPlan subscription. Request id: %x", i%3, 0xabcdef00+i)
	}
	// 25 errors over 48 min; request id / account digits vary per row.
	series := func(acc int64, n int, start time.Time, msg func(int) string) {
		for i := 0; i < n; i++ {
			insErr(acc, start.Add(time.Duration(i)*2*time.Minute), msg(i))
		}
	}

	// expired: old success, then an open 25-row streak → reported.
	insOK(expired, now.Add(-3*time.Hour))
	series(expired, 25, now.Add(-50*time.Minute), expiredMsg)

	// broken: 25-row streak, but a different 400 signature lands last → only the
	// newest run (1 row) is open → not reported.
	series(broken, 25, now.Add(-60*time.Minute), expiredMsg)
	insErr(broken, now.Add(-5*time.Minute), "a parameter specified in the request is not valid")

	// recovered: streak then a success → not reported.
	series(recovered, 25, now.Add(-60*time.Minute), expiredMsg)
	insOK(recovered, now.Add(-2*time.Minute))

	// short: 30 identical rows within 1 minute (single-user retry storm) → span gate.
	for i := 0; i < 30; i++ {
		insErr(short, now.Add(-10*time.Minute).Add(time.Duration(i)*2*time.Second), "a parameter specified in the request is not valid")
	}

	// mixed: only 19 rows → count gate.
	series(mixed, 19, now.Add(-50*time.Minute), expiredMsg)

	// disabled: ops already stopped it (status=error) → not reported.
	disabled := mk("st-disabled")
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET status='error' WHERE id=$1", disabled)
	require.NoError(t, err)
	series(disabled, 25, now.Add(-50*time.Minute), expiredMsg)

	got, err := repo.ListOpenUpstream400Streaks(ctx, now.Add(-6*time.Hour), 20, 10*time.Minute)
	require.NoError(t, err)
	require.Len(t, got, 1, "%+v", got)
	require.Equal(t, expired, got[0].AccountID)
	require.Equal(t, 25, got[0].Count)
	require.Equal(t, 1, got[0].DistinctUsers)
	require.Contains(t, got[0].SampleMessage, "AgentPlan")
	require.NotContains(t, got[0].Signature, "2131399", "digits collapse into the signature")
	_ = broken
	_ = recovered
	_ = short
	_ = mixed
}
