//go:build unit

package qa

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

type memBlobStore struct {
	objects map[string][]byte
}

func newMemBlobStore() *memBlobStore {
	return &memBlobStore{objects: map[string][]byte{}}
}

func (m *memBlobStore) Put(_ context.Context, key string, body []byte, _ string) (string, error) {
	m.objects[key] = append([]byte(nil), body...)
	return "mem://" + key, nil
}

func (m *memBlobStore) PutReader(_ context.Context, key string, reader io.Reader, _ string) (string, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	m.objects[key] = body
	return "mem://" + key, nil
}

func (m *memBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	body, ok := m.objects[key]
	if !ok {
		return nil, io.EOF
	}
	return append([]byte(nil), body...), nil
}

func (m *memBlobStore) Delete(_ context.Context, key string) error {
	delete(m.objects, key)
	return nil
}

func (m *memBlobStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://mem.example/qa/" + key, nil
}

func newQAExportTestService(t *testing.T) (*Service, *dbent.Client, *memBlobStore) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	driver := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() { _ = client.Close() })

	store := newMemBlobStore()
	return NewServiceForTest(client, store), client, store
}

type dlqOnlyBlobStore struct{}

func (dlqOnlyBlobStore) Put(context.Context, string, []byte, string) (string, error) {
	return "", errors.New("primary store unavailable")
}

func (dlqOnlyBlobStore) PutReader(context.Context, string, io.Reader, string) (string, error) {
	return "", errors.New("primary store unavailable")
}

func (dlqOnlyBlobStore) Get(context.Context, string) ([]byte, error) {
	return nil, io.EOF
}

func (dlqOnlyBlobStore) Delete(context.Context, string) error {
	return nil
}

func (dlqOnlyBlobStore) PresignURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
