//go:build integration

package bundle

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Every run owns a fresh PostgreSQL. There is no production/test DSN override.
func queueTestDatabase(t *testing.T) (*sql.DB, string, func()) {
	t.Helper()
	var dsn string
	var restart func()
	if _, err := exec.LookPath("initdb"); err == nil && os.Getuid() != 0 {
		root, err := os.MkdirTemp("/tmp", "tkqa-integration-")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
		data := filepath.Join(root, "pg")
		run := func(args ...string) {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Env = append(os.Environ(), "PGHOST="+root, "PGPORT=55432", "PGUSER=qa_test_admin", "PGDATABASE=postgres")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
		}
		run("initdb", "-D", data, "-U", "qa_test_admin", "--auth=trust", "--locale=C", "-E", "UTF8")
		run("pg_ctl", "-D", data, "-l", filepath.Join(root, "postgres.log"), "-o", "-c listen_addresses='' -k "+root+" -p 55432", "-w", "start")
		t.Cleanup(func() { run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop") })
		restart = func() {
			run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop")
			run("pg_ctl", "-D", data, "-l", filepath.Join(root, "postgres.log"), "-o", "-c listen_addresses='' -k "+root+" -p 55432", "-w", "start")
		}
		dsn = "postgres://qa_test_admin@/postgres?host=" + url.QueryEscape(root) + "&port=55432&sslmode=disable"
		admin, err := sql.Open("postgres", dsn)
		require.NoError(t, err)
		_, err = admin.Exec("CREATE DATABASE tokenkey_qa_queue")
		require.NoError(t, err)
		require.NoError(t, admin.Close())
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		container, err := postgres.Run(ctx, "postgres:18.1-alpine3.23", postgres.WithDatabase("tokenkey_qa_queue"), postgres.WithUsername("qa_test_admin"), postgres.WithPassword("synthetic-test-password"), postgres.BasicWaitStrategies())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
		restart = func() {
			require.NoError(t, container.Stop(context.Background(), nil))
			require.NoError(t, container.Start(context.Background()))
		}
		dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		require.NoError(t, err)
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.Path = "/tokenkey_qa_queue"
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ddl, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "deploy", "hetzner", "qa", "queue.sql"))
	require.NoError(t, err)
	_, err = db.Exec(strings.ReplaceAll(string(ddl), "\\set ON_ERROR_STOP on\n", ""))
	require.NoError(t, err)
	return db, u.String(), restart
}

func TestUS062_PostgresQueueRoleIsolationLeasesAndSingleConcurrency(t *testing.T) {
	admin, dsn, restart := queueTestDatabase(t)
	ctx := context.Background()
	roleQueue := func(role, group string) *postgresJobQueue {
		_, err := admin.Exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'synthetic-test-password' IN ROLE %s", role, group))
		require.NoError(t, err)
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		u.User = url.UserPassword(role, "synthetic-test-password")
		db, err := sql.Open("postgres", u.String())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		return &postgresJobQueue{db: db}
	}
	producer := roleQueue("qa_test_producer", "qa_queue_producer")
	consumer := roleQueue("qa_test_consumer", "qa_queue_consumer")
	require.NoError(t, validateQueueDatabase(ctx, producer.db))
	require.NoError(t, validateQueueDatabase(ctx, consumer.db))
	require.Error(t, validateQueueDatabase(ctx, admin))
	key := func(n int) string { return fmt.Sprintf("qa-bundles/v1/jobs/%064x/spec.json", n) }
	for _, db := range []*sql.DB{producer.db, consumer.db} {
		_, err := db.Exec("SELECT * FROM qa_queue.jobs")
		require.Error(t, err)
		_, err = db.Exec("UPDATE qa_queue.control SET paused=true")
		require.Error(t, err)
		_, err = db.Exec("CREATE TABLE public.forbidden(id int)")
		require.Error(t, err)
	}
	_, err := producer.db.Exec("SELECT * FROM qa_queue.claim()")
	require.Error(t, err)
	require.Error(t, consumer.Enqueue(ctx, key(99)))
	require.Error(t, producer.Enqueue(ctx, "../../foreign"))
	require.NoError(t, producer.Enqueue(ctx, key(1)))
	require.NoError(t, producer.Enqueue(ctx, key(1)))
	message, ok, err := consumer.Receive(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, message.ReceiveCount)
	for i := 2; i <= 10; i++ {
		require.NoError(t, producer.Enqueue(ctx, key(i)))
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var ignored string
			err := consumer.db.QueryRow("SELECT spec_key FROM qa_queue.claim()").Scan(&ignored)
			if err != sql.ErrNoRows {
				t.Errorf("parallel consumer bypassed single lease: %v", err)
			}
		}()
	}
	wg.Wait()
	_, err = admin.Exec("UPDATE qa_queue.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE spec_key=$1", key(1))
	require.NoError(t, err)
	require.Error(t, consumer.Ack(ctx, message))
	redelivery, ok, err := consumer.Receive(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, key(1), redelivery.SpecKey)
	require.Equal(t, 2, redelivery.ReceiveCount)
	require.NotEqual(t, message.ReceiptHandle, redelivery.ReceiptHandle)
	require.Error(t, consumer.Ack(ctx, message))
	require.NoError(t, consumer.Ack(ctx, redelivery))
	require.Error(t, consumer.Ack(ctx, redelivery))
	// Poison jobs remain durable and stop consuming delivery attempts after three.
	for attempt := 1; attempt <= 3; attempt++ {
		m, ok, err := consumer.Receive(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, key(2), m.SpecKey)
		require.Equal(t, attempt, m.ReceiveCount)
		_, err = admin.Exec("UPDATE qa_queue.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE spec_key=$1", key(2))
		require.NoError(t, err)
	}
	next, ok, err := consumer.Receive(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, key(3), next.SpecKey)
	var state string
	require.NoError(t, admin.QueryRow("SELECT state FROM qa_queue.jobs WHERE spec_key=$1", key(2)).Scan(&state))
	require.Equal(t, "dead", state)
	// Pausing prevents new claims but permits the in-flight job to finish.
	_, err = admin.Exec("UPDATE qa_queue.control SET paused=true")
	require.NoError(t, err)
	require.NoError(t, consumer.Ack(ctx, next))
	pausedCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, ok, err = consumer.Receive(pausedCtx)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = admin.Exec("UPDATE qa_queue.control SET paused=false")
	require.NoError(t, err)
	_, ok, err = consumer.Receive(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	// The transition from no active lease must also be serialized, not just
	// claims arriving after a worker already holds the lease.
	_, err = admin.Exec("UPDATE qa_queue.jobs SET state='done', receipt=NULL, lease_until=NULL WHERE state='leased'")
	require.NoError(t, err)
	claims := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var key string
			err := consumer.db.QueryRow("SELECT spec_key FROM qa_queue.claim()").Scan(&key)
			if err == nil {
				claims <- key
			} else if err != sql.ErrNoRows {
				t.Errorf("concurrent claim: %v", err)
			}
		}()
	}
	wg.Wait()
	close(claims)
	var claimed []string
	for key := range claims {
		claimed = append(claimed, key)
	}
	require.Equal(t, []string{key(5)}, claimed)
	var before, after string
	require.NoError(t, admin.QueryRow("SELECT json_agg(j ORDER BY spec_key)::text FROM qa_queue.jobs j").Scan(&before))
	restart()
	require.Eventually(t, func() bool {
		var alive int
		return admin.QueryRow("SELECT 1").Scan(&alive) == nil && alive == 1
	}, 10*time.Second, 50*time.Millisecond)
	require.NoError(t, admin.QueryRow("SELECT json_agg(j ORDER BY spec_key)::text FROM qa_queue.jobs j").Scan(&after))
	require.Equal(t, before, after, "queue state and receipts must survive a PostgreSQL restart")
	// Exercise the producer after the crash too: this verifies reconnection and
	// evicts its stale idle socket before cleanup closes the pool.
	require.NoError(t, producer.Enqueue(ctx, key(11)))
	var retained JobMessage
	require.NoError(t, admin.QueryRow("SELECT spec_key, receipt::text FROM qa_queue.jobs WHERE state='leased'").Scan(&retained.SpecKey, &retained.ReceiptHandle))
	require.NoError(t, consumer.Ack(ctx, retained))

}
