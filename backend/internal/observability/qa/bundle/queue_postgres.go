package bundle

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type postgresJobQueue struct {
	db *sql.DB
}

func readQueueDSN(path string) (string, error) {
	f, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return "", errors.New("open QA queue credential file")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return "", errors.New("QA queue credential must be a private regular file (0600 or 0400)")
	}
	body := make([]byte, info.Size())
	if _, err := f.ReadAt(body, 0); err != nil {
		return "", errors.New("read QA queue credential file")
	}
	dsn := strings.TrimSpace(string(body))
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" ||
		u.User == nil || u.User.Username() == "" || u.Path != "/tokenkey_qa_queue" || u.Query().Get("sslmode") != "verify-full" || u.Fragment != "" {
		return "", errors.New("QA queue requires a PostgreSQL URL for tokenkey_qa_queue with sslmode=verify-full")
	}
	// Prevent URL query parameters from overriding the validated identity/TLS.
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("invalid QA queue connection options")
	}
	for key, values := range query {
		if len(values) != 1 {
			return "", errors.New("duplicate QA queue connection option")
		}
		switch key {
		case "sslmode", "sslrootcert", "sslcert", "sslkey", "connect_timeout":
		default:
			return "", errors.New("unsupported QA queue connection option")
		}
	}
	return dsn, nil
}

func openPostgresJobQueue(ctx context.Context, path string) (*postgresJobQueue, error) {
	dsn, err := readQueueDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.New("configure QA queue database")
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Minute)
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := validateQueueDatabase(checkCtx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &postgresJobQueue{db: db}, nil
}

func validateQueueDatabase(ctx context.Context, db *sql.DB) error {
	var version int
	var database string
	var privileged bool
	err := db.QueryRowContext(ctx, `SELECT current_database(), qa_queue.version(),
    rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls
      OR pg_has_role(current_user, 'qa_queue_owner', 'MEMBER')
    FROM pg_catalog.pg_roles WHERE rolname=current_user`).Scan(&database, &version, &privileged)
	if err != nil || database != "tokenkey_qa_queue" || version != 1 || privileged {
		return errors.New("QA queue requires schema v1 in its dedicated database and an unprivileged runtime role")
	}
	return nil
}

func (q *postgresJobQueue) Close() error { return q.db.Close() }

func (q *postgresJobQueue) Enqueue(ctx context.Context, key string) error {
	if !jobSpecKeyPattern.MatchString(key) {
		return errors.New("qa bundle job spec key is invalid")
	}
	_, err := q.db.ExecContext(ctx, "SELECT qa_queue.enqueue($1)", key)
	return err
}

func (q *postgresJobQueue) Receive(ctx context.Context) (JobMessage, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		var message JobMessage
		err := q.db.QueryRowContext(ctx, "SELECT spec_key, receipt::text, attempts FROM qa_queue.claim()").Scan(&message.SpecKey, &message.ReceiptHandle, &message.ReceiveCount)
		if err == nil {
			message.MessageID = message.SpecKey
			return message, true, nil
		}
		if ctx.Err() != nil {
			return JobMessage{}, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return JobMessage{}, false, err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return JobMessage{}, false, nil
		case <-timer.C:
		}
	}
}

func (q *postgresJobQueue) Ack(ctx context.Context, message JobMessage) error {
	if !jobSpecKeyPattern.MatchString(message.SpecKey) {
		return errors.New("invalid QA queue job key")
	}
	if _, err := uuid.Parse(message.ReceiptHandle); err != nil {
		return errors.New("invalid QA queue receipt")
	}
	var acknowledged bool
	if err := q.db.QueryRowContext(ctx, "SELECT qa_queue.ack($1, $2::uuid)", message.SpecKey, message.ReceiptHandle).Scan(&acknowledged); err != nil {
		return err
	}
	if !acknowledged {
		return errors.New("QA queue receipt is stale or expired")
	}
	return nil
}
