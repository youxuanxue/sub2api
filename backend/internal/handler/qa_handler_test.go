//go:build unit

package handler

// API-key trajectory export handler tests.

import (
	"archive/zip"
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

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/klauspost/compress/zstd"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type qaMemBlobStore struct{ objects map[string][]byte }

func (m *qaMemBlobStore) Put(_ context.Context, key string, body []byte, _ string) (string, error) {
	cp := make([]byte, len(body))
	copy(cp, body)
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	m.objects[key] = cp
	return "mem://" + key, nil
}
func (m *qaMemBlobStore) PutReader(_ context.Context, key string, r io.Reader, _ string) (string, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	m.objects[key] = body
	return "mem://" + key, nil
}
func (m *qaMemBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.objects[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}
func (m *qaMemBlobStore) Delete(_ context.Context, _ string) error { return nil }
func (m *qaMemBlobStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://mem.example/" + key, nil
}

type qaLocalFSLikeBlobStore struct{ qaMemBlobStore }

func (m *qaLocalFSLikeBlobStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "file:///app/data/qa_blobs/" + key, nil
}

func newQAHandlerTestEnv(t *testing.T, withAuth bool, userID int64) (*gin.Engine, *dbent.Client, *QAHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := sql.Open("sqlite", "file:qa_handler_test?mode=memory&cache=shared")
	require.NoError(t, err)
	// Serialize access: the async export worker writes job rows concurrently
	// with the test polling them; a single connection avoids in-memory sqlite
	// "database is locked" flakes.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	store := &qaMemBlobStore{}
	r, h := newQAHandlerRouterWithStore(userID, withAuth, client, store)
	return r, client, h
}

func newQAHandlerRouterWithStore(
	userID int64,
	withAuth bool,
	client *dbent.Client,
	store qa.BlobStore,
) (*gin.Engine, *QAHandler) {
	// NOTE: we hand-build the qa.Service here instead of going through
	// NewService — that constructor expects a real config + S3 driver
	// or local-fs path. A stub blob store is the cheapest possible fixture.
	svc := qa.NewServiceForTest(client, store)
	h := NewQAHandler(svc)

	r := gin.New()
	if withAuth {
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
			c.Next()
		})
	}
	r.POST("/api/v1/users/me/qa/traj/export", h.ExportSelfTrajectory)
	r.GET("/api/v1/users/me/qa/traj/export/jobs", h.ListSelfTrajectoryExports)
	r.GET("/api/v1/users/me/qa/traj/export/jobs/:job_id", h.GetSelfTrajectoryExportJob)
	r.GET("/api/v1/users/me/qa/traj/exports/*key", h.DownloadSelfTrajectoryExport)
	return r, h
}

// pollTrajExport POSTs an async traj export, then polls the job endpoint until
// the job reaches a terminal state, returning the final job response data map.
// The poll request carries the https host so a done job's localfs download_url
// is rewritten to an absolute, client-reachable URL.
func pollTrajExport(t *testing.T, r *gin.Engine, jsonBody string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/traj/export", bytes.NewBufferString(jsonBody))
	req.Host = "api.tokenkey.test"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "enqueue body=%s", w.Body.String())
	var env response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	data, _ := env.Data.(map[string]any)
	jobID, _ := data["job_id"].(string)
	require.NotEmpty(t, jobID, "POST must return a job_id")

	for i := 0; i < 400; i++ {
		gw := httptest.NewRecorder()
		greq := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/qa/traj/export/jobs/"+jobID, nil)
		greq.Host = "api.tokenkey.test"
		greq.Header.Set("X-Forwarded-Proto", "https")
		r.ServeHTTP(gw, greq)
		require.Equal(t, http.StatusOK, gw.Code, "poll body=%s", gw.Body.String())
		var genv response.Response
		require.NoError(t, json.Unmarshal(gw.Body.Bytes(), &genv))
		gdata, _ := genv.Data.(map[string]any)
		if s, _ := gdata["status"].(string); s == "done" || s == "failed" {
			return gdata
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("traj export job did not reach a terminal state in time")
	return nil
}

// seedTrajExportUser inserts a user with the given traj_export_enabled flag and
// returns its (auto-assigned) ID — the trajectory export endpoint now gates on
// this admin-granted switch, so success-path tests must seed an authorized user.
func seedTrajExportUser(t *testing.T, ctx context.Context, client *dbent.Client, enabled bool) int64 {
	t.Helper()
	u, err := client.User.Create().
		SetEmail("traj-export@test.local").
		SetPasswordHash("x").
		SetTrajExportEnabled(enabled).
		Save(ctx)
	require.NoError(t, err)
	return u.ID
}

func seedTrajExportAPIKey(t *testing.T, ctx context.Context, client *dbent.Client, userID int64, platform string) int64 {
	t.Helper()
	group, err := client.Group.Create().
		SetName("trajectory export " + platform + " group " + strconv.FormatInt(userID, 10)).
		SetPlatform(platform).
		Save(ctx)
	require.NoError(t, err)
	key, err := client.APIKey.Create().
		SetUserID(userID).
		SetGroupID(group.ID).
		SetKey("sk-traj-export-" + strconv.FormatInt(userID, 10)).
		SetName("trajectory export test key").
		Save(ctx)
	require.NoError(t, err)
	return key.ID
}

func TestExportSelfTrajectory_ByAPIKeyID_200(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 7)
	ctx := context.Background()
	now := time.Now().UTC()
	uid := seedTrajExportUser(t, ctx, client, true)
	apiKeyID := seedTrajExportAPIKey(t, ctx, client, uid, "anthropic")

	store := &qaMemBlobStore{objects: map[string][]byte{}}
	blob := bytes.Buffer{}
	enc, err := zstd.NewWriter(&blob)
	require.NoError(t, err)
	_, err = enc.Write([]byte(`{"request":{"path":"/v1/messages","body":{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup_weather","input_schema":{"type":"object"}}]}},"response":{"status_code":200,"headers":{},"body":{"content":[{"type":"text","text":"done"}],"tool_calls":[{"id":"call_1","name":"lookup_weather","arguments":{"city":"Paris"}}]}},"stream":{"chunks":[]}}`))
	require.NoError(t, err)
	require.NoError(t, enc.Close())
	store.objects["evidence/traj-handler.zst"] = blob.Bytes()

	_, err = client.QARecord.Create().
		SetRequestID("traj-handler").
		SetUserID(uid).
		SetAPIKeyID(apiKeyID).
		SetPlatform("anthropic").
		SetSynthSessionID("m0-TRAJ-H").
		SetSynthRole("user-simulator").
		SetDialogSynth(true).
		SetBlobURI("mem://evidence/traj-handler.zst").
		SetCreatedAt(now).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	r, _ = newQAHandlerRouterWithStore(uid, true, client, store)
	job := pollTrajExport(t, r, `{"api_key_id":`+strconv.FormatInt(apiKeyID, 10)+`,"format":"v2"}`)
	require.Equal(t, "done", job["status"], "job=%v", job)
	require.Equal(t, float64(1), job["record_count"])
	require.Contains(t, job, "download_url")
}

func TestUS077_ExportSelfTrajectory_LocalFSDownloadURLIsHTTPReachable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", "file:qa_handler_traj_localfs_test?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	uid := seedTrajExportUser(t, ctx, client, true)
	apiKeyID := seedTrajExportAPIKey(t, ctx, client, uid, "anthropic")
	blob := bytes.Buffer{}
	enc, err := zstd.NewWriter(&blob)
	require.NoError(t, err)
	_, err = enc.Write([]byte(`{"request":{"path":"/v1/messages","body":{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup_weather","input_schema":{"type":"object"}}]}},"response":{"status_code":200,"headers":{},"body":{"content":[{"type":"text","text":"done"}],"tool_calls":[{"id":"call_1","name":"lookup_weather","arguments":{"city":"Paris"}}]}},"stream":{"chunks":[]}}`))
	require.NoError(t, err)
	require.NoError(t, enc.Close())

	_, err = client.QARecord.Create().
		SetRequestID("traj-localfs").
		SetUserID(uid).
		SetAPIKeyID(apiKeyID).
		SetPlatform("anthropic").
		SetSynthSessionID("m0-TRAJ-HTTP").
		SetSynthRole("user-simulator").
		SetDialogSynth(true).
		SetBlobURI("mem://evidence/traj-localfs.zst").
		SetCreatedAt(now).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	store := &qaLocalFSLikeBlobStore{qaMemBlobStore{objects: map[string][]byte{"evidence/traj-localfs.zst": blob.Bytes()}}}
	r, _ := newQAHandlerRouterWithStore(uid, true, client, store)

	job := pollTrajExport(t, r, `{"api_key_id":`+strconv.FormatInt(apiKeyID, 10)+`,"format":"v2"}`)
	require.Equal(t, "done", job["status"], "job=%v", job)
	downloadURL, ok := job["download_url"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(downloadURL, "https://api.tokenkey.test/api/v1/users/me/qa/traj/exports/"))
	require.NotContains(t, downloadURL, "file://")

	downloadPath := strings.TrimPrefix(downloadURL, "https://api.tokenkey.test")
	downloadReq := httptest.NewRequest(http.MethodGet, downloadPath, nil)
	downloadW := httptest.NewRecorder()
	r.ServeHTTP(downloadW, downloadReq)

	require.Equal(t, http.StatusOK, downloadW.Code, "body=%s", downloadW.Body.String())
	zr, err := zip.NewReader(bytes.NewReader(downloadW.Body.Bytes()), int64(downloadW.Body.Len()))
	require.NoError(t, err)
	require.Len(t, zr.File, 1)
	require.Equal(t, "trajectory.jsonl", zr.File[0].Name)
}

// Per-user authorization backstop: a user whose traj_export_enabled switch is
// off (the default) gets 403 even with a valid auth subject and captured data.
func TestExportSelfTrajectory_RequiresAPIKeyID(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 7)
	ctx := context.Background()
	uid := seedTrajExportUser(t, ctx, client, true)
	r, _ = newQAHandlerRouterWithStore(uid, true, client, &qaMemBlobStore{objects: map[string][]byte{}})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/traj/export", bytes.NewBufferString(`{"format":"v2"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUS044_ExportSelfTrajectory_RejectsForeignAPIKey(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 7)
	ctx := context.Background()
	uid := seedTrajExportUser(t, ctx, client, true)
	other, err := client.User.Create().
		SetEmail("other-traj-export@test.local").
		SetPasswordHash("x").
		Save(ctx)
	require.NoError(t, err)
	foreignKeyID := seedTrajExportAPIKey(t, ctx, client, other.ID, "anthropic")
	r, _ = newQAHandlerRouterWithStore(uid, true, client, &qaMemBlobStore{objects: map[string][]byte{}})

	body := bytes.NewBufferString(`{"api_key_id":` + strconv.FormatInt(foreignKeyID, 10) + `,"format":"v2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/traj/export", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
	count, err := client.QAExportJob.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestUS044_ExportSelfTrajectory_RejectsUnprojectablePlatform(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 7)
	ctx := context.Background()
	uid := seedTrajExportUser(t, ctx, client, true)
	apiKeyID := seedTrajExportAPIKey(t, ctx, client, uid, "unprojectable-test-platform")
	r, _ = newQAHandlerRouterWithStore(uid, true, client, &qaMemBlobStore{objects: map[string][]byte{}})

	body := bytes.NewBufferString(`{"api_key_id":` + strconv.FormatInt(apiKeyID, 10) + `,"format":"v2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/traj/export", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
	count, err := client.QAExportJob.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestUS044_ExportSelfTrajectory_RejectsNoGroupOrDeletedAPIKey(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 7)
	ctx := context.Background()
	uid := seedTrajExportUser(t, ctx, client, true)
	noGroupKey, err := client.APIKey.Create().
		SetUserID(uid).
		SetKey("sk-traj-export-no-group").
		SetName("trajectory export no-group key").
		Save(ctx)
	require.NoError(t, err)
	deletedKeyID := seedTrajExportAPIKey(t, ctx, client, uid, "anthropic")
	require.NoError(t, client.APIKey.DeleteOneID(deletedKeyID).Exec(ctx))
	r, _ = newQAHandlerRouterWithStore(uid, true, client, &qaMemBlobStore{objects: map[string][]byte{}})

	for _, apiKeyID := range []int64{noGroupKey.ID, deletedKeyID} {
		body := bytes.NewBufferString(`{"api_key_id":` + strconv.FormatInt(apiKeyID, 10) + `,"format":"v2"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/traj/export", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, "api_key_id=%d body=%s", apiKeyID, w.Body.String())
	}
	count, err := client.QAExportJob.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestExportSelfTrajectory_Forbidden_WhenSwitchOff(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 7)
	ctx := context.Background()
	uid := seedTrajExportUser(t, ctx, client, false) // admin switch OFF
	r, _ = newQAHandlerRouterWithStore(uid, true, client, &qaMemBlobStore{objects: map[string][]byte{}})

	body := bytes.NewBufferString(`{"api_key_id":1,"format":"v2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/traj/export", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, "body=%s", w.Body.String())
}

func TestUS044_TrajectoryExportReadSurfaces_ForbiddenWhenSwitchOff(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 7)
	ctx := context.Background()
	uid := seedTrajExportUser(t, ctx, client, false)
	store := &qaMemBlobStore{objects: map[string][]byte{}}
	r, _ = newQAHandlerRouterWithStore(uid, true, client, store)

	jobID := "disabled-user-job"
	key := "traj-exports/" + strconv.FormatInt(uid, 10) + "/1/manual/" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".zip"
	_, err := client.QAExportJob.Create().
		SetJobID(jobID).
		SetUserID(uid).
		SetAPIKeyID(1).
		SetStatus("done").
		SetExportKind("manual").
		SetStorageKey(key).
		SetRecordCount(1).
		SetExpiresAt(time.Now().UTC().Add(time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	store.objects[key] = []byte("zip")

	for _, path := range []string{
		"/api/v1/users/me/qa/traj/export/jobs?api_key_id=1",
		"/api/v1/users/me/qa/traj/export/jobs/" + jobID,
		"/api/v1/users/me/qa/traj/exports/" + key,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, "path=%s body=%s", path, w.Body.String())
	}
}

func TestUS077_DownloadSelfTrajectoryExport_RejectsCrossUserAndTraversalKeys(t *testing.T) {
	r, _, _ := newQAHandlerTestEnv(t, true, 7)

	for _, path := range []string{
		"/api/v1/users/me/qa/traj/exports/traj-exports/8/123.zip",
		"/api/v1/users/me/qa/traj/exports/traj-exports/7/../8/123.zip",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, "path=%s body=%s", path, w.Body.String())
	}
}
