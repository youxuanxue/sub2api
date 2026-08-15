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
	"strings"
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
	group, err := client.Group.Create().SetName("qa bundle " + platform + " " + strconv.FormatInt(userID, 10)).SetPlatform(platform).Save(ctx)
	require.NoError(t, err)
	key, err := client.APIKey.Create().SetUserID(userID).SetGroupID(group.ID).
		SetKey("sk-qa-bundle-" + strconv.FormatInt(userID, 10) + "-" + platform).SetName("qa bundle key").Save(ctx)
	require.NoError(t, err)
	return key.ID
}

func (q *handlerBundleQueue) Enqueue(_ context.Context, key string) error {
	q.keys = append(q.keys, key)
	return nil
}

func registerQABundleHandlerRoutes(r *gin.Engine, h *QAHandler) {
	r.POST("/api/v1/users/me/qa/bundles", h.CreateSelfQABundle)
	r.GET("/api/v1/users/me/qa/bundles/:job_id", h.GetSelfQABundle)
	r.POST("/api/v1/users/me/qa/bundles/:job_id/export", h.CreateSelfQABundleExport)
	r.GET("/api/v1/users/me/qa/bundle-exports/:job_id", h.GetSelfQABundleExport)
}

func configureQABundleHandler(t *testing.T, h *QAHandler) (*handlerBundleQueue, time.Time) {
	t.Helper()
	queue := &handlerBundleQueue{}
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	h.service.ConfigureBundleForTest(
		bundle.NewArchiveStore(archive.NewMemoryObjectStore()), queue,
		func(context.Context) (time.Time, error) { return watermark, nil },
		h.service.UserMayExportAPIKey,
		&qaMemBlobStore{},
	)
	return queue, watermark
}

func TestQABundleHandlersCreateAndReadScopedPendingJob(t *testing.T) {
	r, client, h := newQAHandlerTestEnv(t, true, 1)
	ctx := context.Background()
	userID := seedTrajExportUser(t, ctx, client, true)
	require.Equal(t, int64(1), userID)
	apiKeyID := seedTrajExportAPIKey(t, ctx, client, userID, "anthropic")
	queue, _ := configureQABundleHandler(t, h)
	registerQABundleHandlerRoutes(r, h)

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

func TestQABundleHandlersDenyAllSurfacesWhenEntitlementIsOff(t *testing.T) {
	r, client, h := newQAHandlerTestEnv(t, true, 1)
	userID := seedTrajExportUser(t, context.Background(), client, false)
	require.Equal(t, int64(1), userID)
	queue, _ := configureQABundleHandler(t, h)
	registerQABundleHandlerRoutes(r, h)

	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/v1/users/me/qa/bundles", `{"api_key_id":1}`},
		{http.MethodGet, "/api/v1/users/me/qa/bundles/" + strings.Repeat("a", 64), ""},
		{http.MethodPost, "/api/v1/users/me/qa/bundles/" + strings.Repeat("a", 64) + "/export", ""},
		{http.MethodGet, "/api/v1/users/me/qa/bundle-exports/" + strings.Repeat("b", 64), ""},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(request.method, request.path, bytes.NewBufferString(request.body))
		if request.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, "path=%s body=%s", request.path, w.Body.String())
	}
	require.Empty(t, queue.keys)
}

func TestCreateSelfQABundleRejectsIneligibleAPIKeysWithoutEnqueue(t *testing.T) {
	r, client, h := newQAHandlerTestEnv(t, true, 1)
	ctx := context.Background()
	userID := seedTrajExportUser(t, ctx, client, true)
	other, err := client.User.Create().SetEmail("other-qa-bundle@test.local").SetPasswordHash("x").Save(ctx)
	require.NoError(t, err)
	foreignKeyID := seedTrajExportAPIKey(t, ctx, client, other.ID, "anthropic")
	deletedKeyID := seedTrajExportAPIKey(t, ctx, client, userID, "anthropic")
	require.NoError(t, client.APIKey.DeleteOneID(deletedKeyID).Exec(ctx))
	noGroupKey, err := client.APIKey.Create().SetUserID(userID).SetKey("sk-qa-bundle-no-group").SetName("no group").Save(ctx)
	require.NoError(t, err)
	unprojectableKeyID := seedTrajExportAPIKey(t, ctx, client, userID, "unprojectable-test-platform")
	queue, _ := configureQABundleHandler(t, h)
	registerQABundleHandlerRoutes(r, h)

	for _, apiKeyID := range []int64{foreignKeyID, deletedKeyID, noGroupKey.ID, unprojectableKeyID} {
		w := httptest.NewRecorder()
		body := bytes.NewBufferString(`{"api_key_id":` + strconv.FormatInt(apiKeyID, 10) + `}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/bundles", body)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, "api_key_id=%d body=%s", apiKeyID, w.Body.String())
	}
	require.Empty(t, queue.keys)
}

func TestQABundleHandlersRejectCrossUserJobs(t *testing.T) {
	r, client, h := newQAHandlerTestEnv(t, true, 1)
	ctx := context.Background()
	userID := seedTrajExportUser(t, ctx, client, true)
	other, err := client.User.Create().SetEmail("cross-user-qa-bundle@test.local").SetPasswordHash("x").SetTrajExportEnabled(true).Save(ctx)
	require.NoError(t, err)
	otherKeyID := seedTrajExportAPIKey(t, ctx, client, other.ID, "anthropic")
	queue, watermark := configureQABundleHandler(t, h)
	registerQABundleHandlerRoutes(r, h)
	otherBundle, err := h.service.CreateUserBundle(ctx, other.ID, otherKeyID)
	require.NoError(t, err)
	otherExportID := strings.Repeat("b", 64)
	_, err = client.QAExportJob.Create().
		SetJobID(otherExportID).SetUserID(other.ID).SetAPIKeyID(otherKeyID).
		SetStatus("pending").SetExportKind(string(bundle.JobKindBundleZip)).SetFormat("zip").
		SetWindowStart(watermark.Add(-24 * time.Hour)).SetWindowEnd(watermark).
		SetStorageKey("qa-bundles/v1/jobs/" + otherExportID + "/export.zip").Save(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), userID)

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/users/me/qa/bundles/" + otherBundle.ID},
		{http.MethodPost, "/api/v1/users/me/qa/bundles/" + otherBundle.ID + "/export"},
		{http.MethodGet, "/api/v1/users/me/qa/bundle-exports/" + otherExportID},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(request.method, request.path, nil))
		require.Equal(t, http.StatusNotFound, w.Code, "path=%s body=%s", request.path, w.Body.String())
	}
	require.Len(t, queue.keys, 1, "denied cross-user export create must not enqueue")
}

func TestQABundleHandlersRejectMalformedJobIDs(t *testing.T) {
	r, client, h := newQAHandlerTestEnv(t, true, 1)
	userID := seedTrajExportUser(t, context.Background(), client, true)
	require.Equal(t, int64(1), userID)
	queue, _ := configureQABundleHandler(t, h)
	registerQABundleHandlerRoutes(r, h)

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/users/me/qa/bundles/not-a-job"},
		{http.MethodPost, "/api/v1/users/me/qa/bundles/not-a-job/export"},
		{http.MethodGet, "/api/v1/users/me/qa/bundle-exports/not-a-job"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(request.method, request.path, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, "path=%s body=%s", request.path, w.Body.String())
	}
	require.Empty(t, queue.keys)
}
