package trajectory

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	_ "github.com/glebarez/go-sqlite" // Same SQLite driver as the gateway's existing new-api dependency.
)

// The accounting includes entry/key overhead. Once this fixed budget is reached,
// both indexes and fragments move to one private, disposable SQLite file. No
// journal durability is needed: only the completed ZIP is ever published.
const sessionBufferBytes = 8 << 20

type sessionStore struct {
	dir         string
	limit, used int
	values      map[string][]byte
	fragments   map[string][][]byte
	db          *sql.DB
	tx          *sql.Tx
	closed      bool
}

func newSessionStore(dir string) *sessionStore {
	return &sessionStore{dir: dir, limit: sessionBufferBytes, values: make(map[string][]byte), fragments: make(map[string][][]byte)}
}
func (s *sessionStore) get(ctx context.Context, key string) ([]byte, bool, error) {
	if s.closed {
		return nil, false, errors.New("session store is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if s.tx == nil {
		v, ok := s.values[key]
		return v, ok, nil
	}
	var value []byte
	err := s.tx.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return value, err == nil, err
}
func (s *sessionStore) put(ctx context.Context, key string, value []byte) error {
	if s.closed {
		return errors.New("session store is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cost := len(key) + len(value) + 128
	if old, ok := s.values[key]; ok {
		cost -= len(key) + len(old) + 128
	}
	if s.tx == nil && s.used+cost <= s.limit {
		s.values[key] = value
		s.used += cost
		return nil
	}
	if err := s.spill(ctx); err != nil {
		return err
	}
	_, err := s.tx.ExecContext(ctx, "INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", key, value)
	return err
}
func (s *sessionStore) append(ctx context.Context, key string, value []byte) error {
	if s.closed {
		return errors.New("session store is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cost := len(key) + len(value) + 128
	if s.tx == nil && s.used+cost <= s.limit {
		s.fragments[key] = append(s.fragments[key], value)
		s.used += cost
		return nil
	}
	if err := s.spill(ctx); err != nil {
		return err
	}
	_, err := s.tx.ExecContext(ctx, "INSERT INTO fragments(k,v) VALUES(?,?)", key, value)
	return err
}
func (s *sessionStore) each(ctx context.Context, key string, visit func([]byte) error) error {
	if s.closed {
		return errors.New("session store is closed")
	}
	if s.tx == nil {
		for _, value := range s.fragments[key] {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(value); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	rows, err := s.tx.QueryContext(ctx, "SELECT v FROM fragments WHERE k=? ORDER BY seq", key)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value []byte
		if err = rows.Scan(&value); err != nil {
			return err
		}
		if err = visit(value); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (s *sessionStore) spill(ctx context.Context) error {
	if s.tx != nil {
		return nil
	}
	db, err := sql.Open("sqlite", filepath.Join(s.dir, "sessions.sqlite"))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	s.db = db
	// Bound SQLite's page cache and avoid a second copy of the disposable data.
	for _, query := range []string{"PRAGMA journal_mode=OFF", "PRAGMA synchronous=OFF", "PRAGMA cache_size=-2048", "PRAGMA temp_store=FILE", "PRAGMA mmap_size=0", "CREATE TABLE kv(k TEXT PRIMARY KEY,v BLOB) WITHOUT ROWID", "CREATE TABLE fragments(seq INTEGER PRIMARY KEY,k TEXT,v BLOB)", "CREATE INDEX fragment_order ON fragments(k,seq)"} {
		if _, err = db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	s.tx = tx
	for key, value := range s.values {
		if _, err = tx.ExecContext(ctx, "INSERT INTO kv(k,v) VALUES(?,?)", key, value); err != nil {
			return err
		}
	}
	for key, values := range s.fragments {
		for _, value := range values {
			if _, err = tx.ExecContext(ctx, "INSERT INTO fragments(k,v) VALUES(?,?)", key, value); err != nil {
				return err
			}
		}
	}
	s.values = nil
	s.fragments = nil
	s.used = 0
	return nil
}
func (s *sessionStore) close() error {
	s.closed = true
	s.values = nil
	s.fragments = nil
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
	if s.db != nil {
		err := s.db.Close()
		s.db = nil
		return err
	}
	return nil
}
