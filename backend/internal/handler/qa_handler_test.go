//go:build unit

package handler

// Issue #59 Gap 1 — POST /api/v1/users/me/qa/export handler tests.
//
// We exercise the REAL `QAHandler.ExportSelf` against a real
// `*qa.Service` wired to an in-memory ent client and an in-memory blob
// store. This pins the handler contract end-to-end:
//
//   - 401 when the auth subject is missing (defense-in-depth: middleware
//     already enforces JWT, but the handler MUST also reject so an
//     accidental route mis-mount can't leak data)
//   - 503 when QA capture is disabled in this environment (see
//     observability/qa/service.go Enabled())
//   - 200 with the documented {download_url, expires_at, record_count}
//     envelope on success
//   - the body's `synth_session_id` field is forwarded to the service
//     filter (verified by record_count delta)

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
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
	r, client, h, _ := newQAHandlerTestEnvWithStore(t, withAuth, userID)
	return r, client, h
}

func newQAHandlerTestEnvWithStore(t *testing.T, withAuth bool, userID int64) (*gin.Engine, *dbent.Client, *QAHandler, *qaMemBlobStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := sql.Open("sqlite", "file:qa_handler_test?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	store := &qaMemBlobStore{}
	r, h := newQAHandlerRouterWithStore(userID, withAuth, client, store)
	return r, client, h, store
}

func readHandlerZipFile(t *testing.T, zr *zip.Reader, name string) []byte {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			require.NoError(t, err)
			b, err := io.ReadAll(rc)
			_ = rc.Close()
			require.NoError(t, err)
			return b
		}
	}
	t.Fatalf("zip missing %q", name)
	return nil
}

func qaBlobStoreKeyForRequestID(createdAt time.Time, requestID string) string {
	prefix := "00"
	if len(requestID) >= 2 {
		prefix = requestID[:2]
	}
	return fmt.Sprintf("%04d/%02d/%02d/%s/%s.json.zst",
		createdAt.Year(), int(createdAt.Month()), createdAt.Day(), prefix, requestID)
}

func putQAZstdBlob(t *testing.T, store *qaMemBlobStore, createdAt time.Time, requestID string) string {
	t.Helper()
	if store.objects == nil {
		store.objects = map[string][]byte{}
	}
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	payload := []byte(`{"request_id":"` + requestID + `","captured_at":"` + createdAt.UTC().Format(time.RFC3339) + `","request":{"path":"/v1/messages","body":{}},"response":{"status_code":200,"headers":{},"body":{}},"stream":{"chunks":[]},"redactions":["logredact"]}`)
	zst := enc.EncodeAll(payload, nil)
	_ = enc.Close()
	key := qaBlobStoreKeyForRequestID(createdAt, requestID)
	store.objects[key] = zst
	return "mem://" + key
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
	r.POST("/api/v1/users/me/qa/export", h.ExportSelf)
	r.GET("/api/v1/users/me/qa/exports/*key", h.DownloadSelfExport)
	return r, h
}

func TestUS059_ExportSelf_Unauthenticated_401(t *testing.T) {
	r, _, _ := newQAHandlerTestEnv(t, false, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestUS059_ExportSelf_DisabledService_503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewQAHandler(nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Next()
	})
	r.POST("/api/v1/users/me/qa/export", h.ExportSelf)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestUS059_ExportSelf_BySynthSessionID_200(t *testing.T) {
	r, client, _, store := newQAHandlerTestEnvWithStore(t, true, 7)
	ctx := context.Background()
	now := time.Now().UTC()

	blobURI := putQAZstdBlob(t, store, now, "r1")
	_, err := client.QARecord.Create().
		SetRequestID("r1").
		SetUserID(7).
		SetAPIKeyID(1).
		SetPlatform("anthropic").
		SetSynthSessionID("m0-XYZ").
		SetSynthRole("user-simulator").
		SetDialogSynth(true).
		SetBlobURI(blobURI).
		SetCreatedAt(now).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.QARecord.Create().
		SetRequestID("r2").
		SetUserID(7).
		SetAPIKeyID(1).
		SetPlatform("anthropic").
		SetCreatedAt(now).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"synth_session_id":"m0-XYZ","format":"json"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/export", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var env response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code)
	dataMap, ok := env.Data.(map[string]any)
	require.True(t, ok, "data should be a JSON object")
	require.Contains(t, dataMap, "download_url")
	require.Contains(t, dataMap, "expires_at")
	require.Contains(t, dataMap, "record_count")
	// Numeric JSON field comes back as float64.
	require.Equal(t, float64(1), dataMap["record_count"], "session filter must isolate exactly the one tagged record")
}

func TestUS059_ExportSelf_DefaultsTo24hWindow(t *testing.T) {
	r, client, _ := newQAHandlerTestEnv(t, true, 9)
	ctx := context.Background()
	now := time.Now().UTC()

	// inside window
	_, err := client.QARecord.Create().
		SetRequestID("in").
		SetUserID(9).
		SetAPIKeyID(1).
		SetPlatform("anthropic").
		SetCreatedAt(now.Add(-1 * time.Hour)).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)
	// outside window (48h ago, default cutoff is 24h)
	_, err = client.QARecord.Create().
		SetRequestID("old").
		SetUserID(9).
		SetAPIKeyID(1).
		SetPlatform("anthropic").
		SetCreatedAt(now.Add(-48 * time.Hour)).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	// Empty body — must default to last 24h.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var env response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	dataMap := env.Data.(map[string]any)
	require.Equal(t, float64(1), dataMap["record_count"], "default window must be 24h, not unbounded")
}

func TestUS059_ExportSelf_BadRequest_InvalidJSON(t *testing.T) {
	r, _, _ := newQAHandlerTestEnv(t, true, 1)
	body := bytes.NewBufferString(`{not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/export", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// Defense in depth: even if a malicious user submits a synth_session_id
// belonging to a different user, the service-layer `WHERE user_id =`
// predicate must return zero records (and the handler must not 500).
func TestUS059_ExportSelf_CannotEscapeUserScope(t *testing.T) {
	r, client, _, store := newQAHandlerTestEnvWithStore(t, true, 100)
	ctx := context.Background()
	now := time.Now().UTC()

	// Victim row would be exportable if user scope were wrong — it must not surface to user 100.
	_, err := client.QARecord.Create().
		SetRequestID("victim").
		SetUserID(200).
		SetAPIKeyID(2).
		SetPlatform("anthropic").
		SetSynthSessionID("m0-VICTIM").
		SetBlobURI(putQAZstdBlob(t, store, now, "victim")).
		SetCreatedAt(now).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"synth_session_id":"m0-VICTIM"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/export", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var env response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	dataMap := env.Data.(map[string]any)
	require.Equal(t, float64(0), dataMap["record_count"],
		"attacker (user 100) must NOT see another user's records even with the right synth_session_id")
}

func TestUS033_ExportSelf_LocalFSDownloadURLIsHTTPReachable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", "file:qa_handler_localfs_test?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	store := &qaLocalFSLikeBlobStore{qaMemBlobStore{objects: map[string][]byte{}}}
	blobURI := putQAZstdBlob(t, &store.qaMemBlobStore, now, "localfs-row")
	_, err = client.QARecord.Create().
		SetRequestID("localfs-row").
		SetUserID(7).
		SetAPIKeyID(1).
		SetPlatform("anthropic").
		SetSynthSessionID("m0-HTTP").
		SetSynthRole("user-simulator").
		SetRequestedModel("claude-sonnet").
		SetUpstreamModel("claude-sonnet-4-5").
		SetInputTokens(3).
		SetOutputTokens(5).
		SetBlobURI(blobURI).
		SetCreatedAt(now).
		SetRetentionUntil(now.Add(7 * 24 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	r, _ := newQAHandlerRouterWithStore(7, true, client, store)

	body := bytes.NewBufferString(`{"synth_session_id":"m0-HTTP"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/qa/export", body)
	req.Host = "api.tokenkey.test"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var env response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	dataMap := env.Data.(map[string]any)
	downloadURL, ok := dataMap["download_url"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(downloadURL, "https://api.tokenkey.test/api/v1/users/me/qa/exports/"))
	require.NotContains(t, downloadURL, "file://", "external SDK clients must receive an HTTP(S) URL")

	downloadPath := strings.TrimPrefix(downloadURL, "https://api.tokenkey.test")
	downloadReq := httptest.NewRequest(http.MethodGet, downloadPath, nil)
	downloadW := httptest.NewRecorder()
	r.ServeHTTP(downloadW, downloadReq)

	require.Equal(t, http.StatusOK, downloadW.Code, "body=%s", downloadW.Body.String())
	require.Equal(t, "application/zip", downloadW.Header().Get("Content-Type"))
	zr, err := zip.NewReader(bytes.NewReader(downloadW.Body.Bytes()), int64(downloadW.Body.Len()))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(zr.File), 3, "manifest + jsonl + blob")
	raw := readHandlerZipFile(t, zr, "qa_records.jsonl")
	var row map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(raw), &row))
	for _, field := range []string{"api_key_id", "upstream_model", "input_tokens", "output_tokens", "synth_session_id"} {
		require.Contains(t, row, field, "M0 D6-required export field must stay snake_case")
	}
	require.Equal(t, "m0-HTTP", row["synth_session_id"])
}

func TestUS033_DownloadSelfExport_RejectsCrossUserAndTraversalKeys(t *testing.T) {
	r, _, _ := newQAHandlerTestEnv(t, true, 7)

	for _, path := range []string{
		"/api/v1/users/me/qa/exports/exports/8/123.zip",
		"/api/v1/users/me/qa/exports/exports/7/../8/123.zip",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, "path=%s body=%s", path, w.Body.String())
	}
}
