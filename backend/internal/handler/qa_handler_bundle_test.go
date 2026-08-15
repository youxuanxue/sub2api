//go:build unit

package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/bundle"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type handlerBundleQueue struct{ keys []string }

type qaMemBlobStore struct{ objects map[string][]byte }

func (m *qaMemBlobStore) Put(_ context.Context, key string, body []byte, _ string) (string, error) {
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	m.objects[key] = append([]byte(nil), body...)
	return "mem://" + key, nil
}

func (m *qaMemBlobStore) PutReader(_ context.Context, key string, body io.Reader, _ string) (string, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	return m.Put(context.Background(), key, payload, "")
}

func (m *qaMemBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	payload, ok := m.objects[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), payload...), nil
}

func (m *qaMemBlobStore) Delete(context.Context, string) error { return nil }

func (m *qaMemBlobStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://mem.example/" + key, nil
}

func newQAHandlerTestEnv(t *testing.T, withAuth bool, userID int64) (*gin.Engine, *dbent.Client, *QAHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", "file:qa_bundle_handler_test?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() { _ = client.Close() })
	service := qa.NewServiceForTest(client, &qaMemBlobStore{})
	handler := NewQAHandler(service)
	router := gin.New()
	if withAuth {
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
			c.Next()
		})
	}
	return router, client, handler
}

func seedTrajExportUser(t *testing.T, ctx context.Context, client *dbent.Client, enabled bool) int64 {
	t.Helper()
	user, err := client.User.Create().SetEmail("qa-bundle@test.local").SetPasswordHash("x").SetTrajExportEnabled(enabled).Save(ctx)
	require.NoError(t, err)
	return user.ID
}

func seedTrajExportAPIKey(t *testing.T, ctx context.Context, client *dbent.Client, userID int64, platform string) int64 {
	t.Helper()
	group, err := client.Group.Create().SetName("qa bundle " + platform).SetPlatform(platform).Save(ctx)
	require.NoError(t, err)
	key, err := client.APIKey.Create().SetUserID(userID).SetGroupID(group.ID).
		SetKey("sk-qa-bundle-" + strconv.FormatInt(userID, 10)).SetName("qa bundle key").Save(ctx)
	require.NoError(t, err)
	return key.ID
}

func (q *handlerBundleQueue) Enqueue(_ context.Context, key string) error {
	q.keys = append(q.keys, key)
	return nil
}

func TestQABundleHandlersCreateAndReadScopedPendingJob(t *testing.T) {
	r, client, h := newQAHandlerTestEnv(t, true, 1)
	ctx := context.Background()
	userID := seedTrajExportUser(t, ctx, client, true)
	require.Equal(t, int64(1), userID)
	apiKeyID := seedTrajExportAPIKey(t, ctx, client, userID, "anthropic")
	objects := archive.NewMemoryObjectStore()
	queue := &handlerBundleQueue{}
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	h.service.ConfigureBundleForTest(
		bundle.NewArchiveStore(objects), queue,
		func(context.Context) (time.Time, error) { return watermark, nil },
		h.service.UserMayExportAPIKey,
		&qaMemBlobStore{},
	)
	r.POST("/api/v1/users/me/qa/bundles", h.CreateSelfQABundle)
	r.GET("/api/v1/users/me/qa/bundles/:job_id", h.GetSelfQABundle)

	requestBody, _ := json.Marshal(map[string]any{"api_key_id": apiKeyID})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/bundles", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var envelope response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	data := envelope.Data.(map[string]any)
	require.Equal(t, "pending", data["status"])
	require.NotEmpty(t, data["job_id"])
	require.Len(t, queue.keys, 1)

	get := httptest.NewRecorder()
	r.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/users/me/qa/bundles/"+data["job_id"].(string), nil))
	require.Equal(t, http.StatusOK, get.Code, get.Body.String())
}
