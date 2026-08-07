//go:build unit

package qa

// QA capture and API-key trajectory export service tests.

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/qarecord"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine"
	"github.com/alitto/pond/v2"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

// memBlobStore is a minimal in-memory BlobStore used only by export tests.
// It records the zip body keyed by storage key so the test can assert the
// zip layout the M0 client will receive.
type memBlobStore struct {
	objects map[string][]byte
}

func newMemBlobStore() *memBlobStore { return &memBlobStore{objects: map[string][]byte{}} }

func (m *memBlobStore) Put(_ context.Context, key string, body []byte, _ string) (string, error) {
	cp := make([]byte, len(body))
	copy(cp, body)
	m.objects[key] = cp
	return "mem://" + key, nil
}
func (m *memBlobStore) PutReader(_ context.Context, key string, r io.Reader, _ string) (string, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	m.objects[key] = body
	return "mem://" + key, nil
}
func (m *memBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.objects[key]
	if !ok {
		return nil, io.EOF
	}
	return v, nil
}
func (m *memBlobStore) Delete(_ context.Context, key string) error {
	delete(m.objects, key)
	return nil
}
func (m *memBlobStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://mem.example/exports/" + key, nil
}

func newQAExportTestService(t *testing.T) (*Service, *dbent.Client, *memBlobStore) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:qa_export_test?mode=memory&cache=shared")
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

	store := newMemBlobStore()
	svc := NewServiceForTest(client, store)
	return svc, client, store
}

func mustInsertQARecord(t *testing.T, ctx context.Context, client *dbent.Client, b qaRecordBuilder) {
	t.Helper()
	create := client.QARecord.Create().
		SetRequestID(b.requestID).
		SetUserID(b.userID).
		SetAPIKeyID(b.apiKeyID).
		SetPlatform(b.platformOrDefault()).
		SetCreatedAt(b.createdAt).
		SetRetentionUntil(b.createdAt.Add(7 * 24 * time.Hour))
	if b.inboundEndpoint != "" {
		create = create.SetInboundEndpoint(b.inboundEndpoint)
	}
	if b.synthSession != "" {
		create = create.SetSynthSessionID(b.synthSession)
	}
	if b.synthRole != "" {
		create = create.SetSynthRole(b.synthRole)
	}
	if b.dialogSynth {
		create = create.SetDialogSynth(true)
	}
	_, err := create.Save(ctx)
	require.NoError(t, err)
}

type qaRecordBuilder struct {
	requestID    string
	userID       int64
	apiKeyID     int64
	createdAt    time.Time
	synthSession string
	synthRole    string
	dialogSynth  bool
	// platform / inboundEndpoint default to anthropic / "" so existing callers
	// are unaffected; multi-platform export tests set them to drive the
	// projector's wire-shape dispatch (WireShapeForRecord).
	platform        string
	inboundEndpoint string
}

func (b qaRecordBuilder) platformOrDefault() string {
	if b.platform != "" {
		return b.platform
	}
	return "anthropic"
}

type dlqOnlyBlobStore struct{}

func (dlqOnlyBlobStore) Put(_ context.Context, _ string, _ []byte, _ string) (string, error) {
	return "", errors.New("primary store unavailable")
}
func (dlqOnlyBlobStore) PutReader(_ context.Context, _ string, _ io.Reader, _ string) (string, error) {
	return "", errors.New("primary store unavailable")
}
func (dlqOnlyBlobStore) Get(_ context.Context, _ string) ([]byte, error) { return nil, io.EOF }
func (dlqOnlyBlobStore) Delete(_ context.Context, _ string) error        { return nil }
func (dlqOnlyBlobStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil
}

func resetQACaptureCounters() {
	qaCaptureSubmittedCount.Store(0)
	qaCaptureAsyncAcceptedCount.Store(0)
	qaCaptureSyncFallbackCount.Store(0)
	qaCapturePersistedCount.Store(0)
	qaCaptureDegradedDLQCount.Store(0)
	qaCapturePersistFailedCount.Store(0)
}

func TestUS075_PersistCapture_DLQDowngradesCaptureStatus(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	resetQACaptureCounters()

	db, err := sql.Open("sqlite", "file:qa_dlq_status_test?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	svc := &Service{
		client: client,
		store:  dlqOnlyBlobStore{},
		cfg:    config.QACaptureConfig{Enabled: true},
		dlqDir: filepath.Join(t.TempDir(), "qa_dlq"),
	}

	err = svc.persistCapture(context.Background(), CaptureInput{
		RequestID:        "capture-dlq-status",
		UserID:           7,
		APIKeyID:         1,
		Platform:         "anthropic",
		RequestedModel:   "claude-sonnet-4-5",
		UpstreamModel:    "claude-sonnet-4-5-20250929",
		StatusCode:       200,
		RedactionVersion: qaRedactionVersion,
		CaptureStatus:    captureStatusCaptured,
		CreatedAt:        time.Now().UTC(),
	})
	require.NoError(t, err)

	record, err := client.QARecord.Query().Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, qaCaptureStatusCapturedToDLQ, record.CaptureStatus)
	require.NotNil(t, record.BlobURI)
	require.True(t, strings.HasPrefix(*record.BlobURI, "dlq://"))
	require.Equal(t, int64(1), qaCaptureDegradedDLQCount.Load())
}

func TestUS076_Submit_QueueFullFallsBackSync(t *testing.T) {
	resetQACaptureCounters()
	svc, client, _ := newQAExportTestService(t)
	svc.pool = pond.NewPool(1, pond.WithQueueSize(0))
	t.Cleanup(func() { svc.Stop() })

	blocker := make(chan struct{})
	_, ok := svc.pool.TrySubmit(func() { <-blocker })
	require.True(t, ok)

	svc.Submit(CaptureInput{
		RequestID:        "capture-sync-fallback",
		UserID:           7,
		APIKeyID:         1,
		Platform:         "anthropic",
		RequestedModel:   "claude-sonnet-4-5",
		StatusCode:       200,
		RedactionVersion: qaRedactionVersion,
		CaptureStatus:    captureStatusCaptured,
		CreatedAt:        time.Now().UTC(),
	})
	close(blocker)

	record, err := client.QARecord.Query().Where(qarecord.RequestIDEQ("capture-sync-fallback")).Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, captureStatusCaptured, record.CaptureStatus)
	require.Equal(t, int64(1), qaCaptureSubmittedCount.Load())
	require.Equal(t, int64(0), qaCaptureAsyncAcceptedCount.Load())
	require.Equal(t, int64(1), qaCaptureSyncFallbackCount.Load())
	require.Equal(t, int64(1), qaCapturePersistedCount.Load())
	require.Equal(t, int64(0), qaCapturePersistFailedCount.Load())
}

func TestUS070_PersistCapture_WritesUpstreamModel(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	const (
		sentinelInputTokens  = 123
		sentinelOutputTokens = 45
		sentinelCachedTokens = 6
	)

	createdAt := time.Now().UTC()
	err := svc.persistCapture(ctx, CaptureInput{
		RequestID:      "capture-upstream-model",
		UserID:         7,
		APIKeyID:       1,
		Platform:       "anthropic",
		RequestedModel: "claude-sonnet-4-5",
		UpstreamModel:  "claude-sonnet-4-5-20250929",
		StatusCode:     200,
		// Sentinel values prove persistCapture stores caller-provided usage
		// exactly; production callers populate them from forward result usage.
		InputTokens:  sentinelInputTokens,
		OutputTokens: sentinelOutputTokens,
		CachedTokens: sentinelCachedTokens,
		CreatedAt:    createdAt,
	})
	require.NoError(t, err)

	record, err := client.QARecord.Query().Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, record.UpstreamModel)
	require.Equal(t, "claude-sonnet-4-5-20250929", *record.UpstreamModel)
	require.Equal(t, sentinelInputTokens, record.InputTokens)
	require.Equal(t, sentinelOutputTokens, record.OutputTokens)
	require.Equal(t, sentinelCachedTokens, record.CachedTokens)
	require.Equal(t, createdAt.Add(24*time.Hour), record.RetentionUntil)
}

func TestUS070_PersistCapture_WritesExtendedMetadata(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	createdAt := time.Now().UTC()
	groupID := int64(23)
	accountID := int64(45)
	channelType := 54
	firstTokenMs := int64(321)

	err := svc.persistCapture(ctx, CaptureInput{
		RequestID:        "capture-extended-metadata",
		TrajectoryID:     "traj-123",
		UserID:           7,
		GroupID:          &groupID,
		APIKeyID:         1,
		AccountID:        &accountID,
		Platform:         "newapi",
		Provider:         engine.ProviderNewAPIBridge,
		ChannelType:      &channelType,
		RequestedModel:   "doubao-video",
		UpstreamModel:    "doubao-video-v1",
		InboundEndpoint:  "/v1/video/generations",
		UpstreamEndpoint: "/v1/video/generations",
		StatusCode:       202,
		Success:          true,
		DurationMs:       987,
		FirstTokenMs:     &firstTokenMs,
		Stream:           true,
		RequestBody:      []byte(`{"model":"doubao-video","input":"hello"}`),
		ResponseBody:     []byte(`{"id":"vt_123","status":"submitted"}`),
		RequestBlobURI:   "",
		ResponseBlobURI:  "",
		StreamBlobURI:    "",
		RedactionVersion: "logredact-v2",
		CaptureStatus:    "captured",
		CreatedAt:        createdAt,
	})
	require.NoError(t, err)

	record, err := client.QARecord.Query().Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, record.GroupID)
	require.Equal(t, groupID, *record.GroupID)
	require.NotNil(t, record.AccountID)
	require.Equal(t, accountID, *record.AccountID)
	require.Equal(t, "newapi", record.Platform)
	require.NotNil(t, record.Provider)
	require.Equal(t, engine.ProviderNewAPIBridge, *record.Provider)
	require.NotNil(t, record.ChannelType)
	require.Equal(t, channelType, *record.ChannelType)
	require.NotNil(t, record.UpstreamModel)
	require.Equal(t, "doubao-video-v1", *record.UpstreamModel)
	require.NotNil(t, record.UpstreamEndpoint)
	require.Equal(t, "/v1/video/generations", *record.UpstreamEndpoint)
	require.True(t, record.Success)
	require.NotNil(t, record.FirstTokenMs)
	require.Equal(t, firstTokenMs, *record.FirstTokenMs)
	require.Equal(t, "logredact-v2", record.RedactionVersion)
	require.Equal(t, "captured", record.CaptureStatus)
	require.NotNil(t, record.BlobURI)
	require.NotNil(t, record.RequestBlobURI)
	require.NotNil(t, record.ResponseBlobURI)
	require.NotNil(t, record.StreamBlobURI)
	require.Equal(t, *record.BlobURI, *record.RequestBlobURI)
	require.Equal(t, *record.BlobURI, *record.ResponseBlobURI)
	require.Equal(t, *record.BlobURI, *record.StreamBlobURI)
}

func encodeEvidenceBlobForTest(t *testing.T, blob map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(blob)
	require.NoError(t, err)
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = enc.Write(raw)
	require.NoError(t, err)
	require.NoError(t, enc.Close())
	return buf.Bytes()
}

func mustInsertQARecordWithBlob(t *testing.T, ctx context.Context, client *dbent.Client, store *memBlobStore, b qaRecordBuilder, blob map[string]any) {
	t.Helper()
	key := "evidence/" + b.requestID + ".zst"
	store.objects[key] = encodeEvidenceBlobForTest(t, blob)
	create := client.QARecord.Create().
		SetRequestID(b.requestID).
		SetUserID(b.userID).
		SetAPIKeyID(b.apiKeyID).
		SetPlatform(b.platformOrDefault()).
		SetCreatedAt(b.createdAt).
		SetRetentionUntil(b.createdAt.Add(7 * 24 * time.Hour)).
		SetBlobURI("mem://" + key)
	if b.inboundEndpoint != "" {
		create = create.SetInboundEndpoint(b.inboundEndpoint)
	}
	if b.synthSession != "" {
		create = create.SetSynthSessionID(b.synthSession)
	}
	if b.synthRole != "" {
		create = create.SetSynthRole(b.synthRole)
	}
	if b.dialogSynth {
		create = create.SetDialogSynth(true)
	}
	_, err := create.Save(ctx)
	require.NoError(t, err)
}

// Per-key export ("导出该 Key 的对话记录") must restrict the record set to a
// single API key while keeping the user_id scope as the outer AND — so a
// foreign user id with the same key id yields zero rows.
func TestExportUserTrajectoryData_ScopedByAPIKey(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	simpleBlob := map[string]any{
		"request":  map[string]any{"path": "/v1/messages", "body": map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}},
		"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}},
		"stream":   map[string]any{"chunks": []any{}},
	}
	// Same user (7), two different API keys: two records under key 1, one under key 2.
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{requestID: "key1-a", userID: 7, apiKeyID: 1, createdAt: now.Add(-2 * time.Hour)}, simpleBlob)
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{requestID: "key1-b", userID: 7, apiKeyID: 1, createdAt: now.Add(-1 * time.Hour)}, simpleBlob)
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{requestID: "key2-a", userID: 7, apiKeyID: 2, createdAt: now.Add(-30 * time.Minute)}, simpleBlob)

	key1 := int64(1)
	recs, err := client.QARecord.Query().Where(svc.exportPredicates(7, ExportFilter{APIKeyID: &key1})...).All(ctx)
	require.NoError(t, err)
	require.Len(t, recs, 2, "only key 1's two records, key 2 excluded")
	for _, r := range recs {
		require.Equal(t, int64(1), r.APIKeyID)
	}

	// Foreign user id with the same key id → zero rows (user scope ANDs first).
	foreign, err := client.QARecord.Query().Where(svc.exportPredicates(8, ExportFilter{APIKeyID: &key1})...).All(ctx)
	require.NoError(t, err)
	require.Empty(t, foreign)

	// End-to-end export scoped by key 1 builds a non-empty trajectory.
	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{APIKeyID: &key1})
	require.NoError(t, err)
	require.Positive(t, res.RecordCount)
}

// Resilience: a single missing/pruned blob (the retention cleanup can delete a
// blob mid-export) must NOT abort the whole export — the record is skipped and
// the surviving records still produce a non-empty zip. Pre-fix this returned an
// error and the user got nothing.
func TestExportUserTrajectoryData_SkipsMissingBlob(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	simpleBlob := map[string]any{
		"request":  map[string]any{"path": "/v1/messages", "body": map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}},
		"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}},
		"stream":   map[string]any{"chunks": []any{}},
	}
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{requestID: "gone", userID: 7, apiKeyID: 1, createdAt: now.Add(-2 * time.Hour)}, simpleBlob)
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{requestID: "kept", userID: 7, apiKeyID: 1, createdAt: now.Add(-1 * time.Hour)}, simpleBlob)

	// Simulate the "gone" record's blob being pruned out from under the export.
	rec, err := client.QARecord.Query().Where(qarecord.RequestIDEQ("gone")).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, rec.BlobURI)
	delete(store.objects, strings.TrimPrefix(*rec.BlobURI, "mem://"))

	key1 := int64(1)
	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{APIKeyID: &key1, Format: "v2"})
	require.NoError(t, err, "missing blob must be skipped, not abort the export")
	require.Equal(t, 1, res.RecordCount, "only the surviving record is counted")
	require.NotEmpty(t, res.StorageKey)
	require.NotEmpty(t, store.objects[res.StorageKey], "zip is non-empty")
}

func TestUS077_ExportUserTrajectoryData_ProjectsSessionTurnAndTools(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{
		requestID:    "traj-1",
		userID:       7,
		apiKeyID:     1,
		createdAt:    now,
		synthSession: "m0-TRAJ",
		synthRole:    "user-simulator",
		dialogSynth:  true,
	}, map[string]any{
		"request": map[string]any{
			"path": "/v1/messages",
			"body": map[string]any{
				"messages": []any{
					map[string]any{"role": "user", "content": "hello"},
					map[string]any{"role": "tool", "tool_call_id": "call_1", "name": "lookup_weather", "content": "sunny"},
				},
				"tools": []any{
					map[string]any{"name": "lookup_weather", "input_schema": map[string]any{"type": "object"}},
				},
			},
		},
		"response": map[string]any{
			"status_code": 200,
			"headers":     map[string]any{},
			"body": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "done"}},
				"tool_calls": []any{
					map[string]any{"id": "call_1", "name": "lookup_weather", "arguments": map[string]any{"city": "Paris"}},
				},
			},
		},
		"stream": map[string]any{"chunks": []any{}},
	})

	key1 := int64(1)
	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{APIKeyID: &key1})
	require.NoError(t, err)
	require.Equal(t, 5, res.RecordCount)
	require.NotEmpty(t, res.StorageKey)

	blob := store.objects[res.StorageKey]
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	require.NoError(t, err)
	require.Len(t, zr.File, 1)
	require.Equal(t, "trajectory.jsonl", zr.File[0].Name)

	rc, err := zr.File[0].Open()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 5)

	kindCount := map[string]int{}
	toolRows := map[string]map[string]any{}
	for _, line := range lines {
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		require.Equal(t, "m0-TRAJ", row["session_id"])
		require.Equal(t, float64(1), row["turn_index"])
		kind, _ := row["message_kind"].(string)
		kindCount[kind]++
		toolRows[kind] = row
	}

	require.Equal(t, 1, kindCount["request"])
	require.Equal(t, 1, kindCount["tool_result"])
	require.Equal(t, 1, kindCount["tool_schema"])
	require.Equal(t, 1, kindCount["response"])
	require.Equal(t, 1, kindCount["tool_call"])
	require.Equal(t, "lookup_weather", toolRows["tool_result"]["tool_name"])
	require.Equal(t, "call_1", toolRows["tool_result"]["tool_call_id"])
	require.Equal(t, "lookup_weather", toolRows["tool_call"]["tool_name"])
	require.Equal(t, "call_1", toolRows["tool_call"]["tool_call_id"])
}

func writeTrajectoryExportFixture(t *testing.T, zipBody []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trajectory_export.zip")
	require.NoError(t, os.WriteFile(path, zipBody, 0o600))
	return path
}

func runQAEvidenceDatasetCheck(t *testing.T, datasetPath string, args ...string) (int, string) {
	t.Helper()
	commandArgs := append([]string{"scripts/checks/qa-evidence-dataset.py", datasetPath}, args...)
	cmd := exec.Command("python3", commandArgs...)
	cmd.Dir = filepath.Join("..", "..", "..", "..")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(output)
	}
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	return exitErr.ExitCode(), string(output)
}

func TestUS077_QAEvidenceDatasetCheck_AcceptsProjectedExportZip(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{
		requestID:    "traj-gate-1",
		userID:       7,
		apiKeyID:     1,
		createdAt:    now,
		synthSession: "m0-GATE",
		synthRole:    "user-simulator",
		dialogSynth:  true,
	}, map[string]any{
		"request": map[string]any{
			"path": "/v1/messages",
			"body": map[string]any{
				"messages": []any{
					map[string]any{"role": "user", "content": "hello"},
					map[string]any{"role": "tool", "tool_call_id": "call_1", "name": "lookup_weather", "content": "sunny"},
				},
				"tools": []any{
					map[string]any{"name": "lookup_weather", "input_schema": map[string]any{"type": "object"}},
				},
			},
		},
		"response": map[string]any{
			"status_code": 200,
			"headers":     map[string]any{},
			"body": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "done"}},
				"tool_calls": []any{
					map[string]any{"id": "call_1", "name": "lookup_weather", "arguments": map[string]any{"city": "Paris"}},
				},
			},
		},
		"stream": map[string]any{"chunks": []any{}},
	})
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{
		requestID:    "traj-gate-2",
		userID:       7,
		apiKeyID:     1,
		createdAt:    now.Add(1 * time.Second),
		synthSession: "m0-GATE",
		synthRole:    "assistant-worker",
		dialogSynth:  true,
	}, map[string]any{
		"request": map[string]any{
			"path": "/v1/messages",
			"body": map[string]any{
				"messages": []any{
					map[string]any{"role": "user", "content": "tell me the weather again"},
					map[string]any{"role": "tool", "tool_call_id": "call_2", "name": "lookup_weather", "content": "cloudy"},
				},
				"tools": []any{
					map[string]any{"name": "lookup_weather", "input_schema": map[string]any{"type": "object"}},
				},
			},
		},
		"response": map[string]any{
			"status_code": 200,
			"headers":     map[string]any{},
			"body": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "cloudy in Paris"}},
				"tool_calls": []any{
					map[string]any{"id": "call_2", "name": "lookup_weather", "arguments": map[string]any{"city": "Paris"}},
				},
			},
		},
		"stream": map[string]any{"chunks": []any{}},
	})

	key1 := int64(1)
	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{APIKeyID: &key1})
	require.NoError(t, err)
	zipPath := writeTrajectoryExportFixture(t, store.objects[res.StorageKey])

	exitCode, output := runQAEvidenceDatasetCheck(t, zipPath)
	require.Equal(t, 0, exitCode, output)
	require.Contains(t, output, "ok: QA evidence dataset passed H1/H2/H3/D1 and structural checks")
}

func TestUS077_QAEvidenceDatasetCheck_RejectsDuplicateTurnDataset(t *testing.T) {
	datasetPath := filepath.Join(t.TempDir(), "duplicate-trajectory.jsonl")
	rows := []map[string]any{
		{"session_id": "m0-DUP", "turn_index": 1, "role": "user", "message_kind": "request", "content_json": []any{"hello"}, "request_id": "req-1"},
		{"session_id": "m0-DUP", "turn_index": 1, "role": "assistant", "message_kind": "response", "content_json": []any{"done"}, "request_id": "req-1"},
		{"session_id": "m0-DUP", "turn_index": 2, "role": "user", "message_kind": "request", "content_json": []any{"hello"}, "request_id": "req-2"},
		{"session_id": "m0-DUP", "turn_index": 2, "role": "assistant", "message_kind": "response", "content_json": []any{"done"}, "request_id": "req-2"},
		{"session_id": "m0-DUP", "turn_index": 2, "role": "assistant", "message_kind": "tool_call", "tool_name": "lookup_weather", "tool_call_id": "call-2", "tool_call_json": map[string]any{"city": "Paris"}, "request_id": "req-2"},
	}
	var lines []string
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		require.NoError(t, err)
		lines = append(lines, string(encoded))
	}
	require.NoError(t, os.WriteFile(datasetPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	exitCode, output := runQAEvidenceDatasetCheck(t, datasetPath)
	require.Equal(t, 1, exitCode, output)
	require.Contains(t, output, "D1 failed")
}

func TestUS077_DownloadUserTrajectoryExport_OwnedKeyOnly(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{
		requestID:    "traj-owned",
		userID:       7,
		apiKeyID:     1,
		createdAt:    now,
		synthSession: "m0-DOWNLOAD",
	}, map[string]any{
		"request":  map[string]any{"path": "/v1/messages", "body": map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}},
		"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}},
		"stream":   map[string]any{"chunks": []any{}},
	})

	key1 := int64(1)
	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{APIKeyID: &key1})
	require.NoError(t, err)
	require.NotEmpty(t, res.StorageKey)

	body, err := svc.DownloadUserTrajectoryExport(ctx, 7, res.StorageKey)
	require.NoError(t, err)
	require.Equal(t, store.objects[res.StorageKey], body)

	_, err = svc.DownloadUserTrajectoryExport(ctx, 8, res.StorageKey)
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrPermission))

	_, err = svc.DownloadUserTrajectoryExport(ctx, 7, "../7/"+res.StorageKey)
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrPermission))

	expiredKey := "traj-exports/7/1.zip"
	store.objects[expiredKey] = []byte("expired")
	_, err = svc.DownloadUserTrajectoryExport(ctx, 7, expiredKey)
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrNotExist))
}

func TestUS077_ExportUserTrajectoryData_UnknownKey_EmptyFails(t *testing.T) {
	svc, _, _ := newQAExportTestService(t)
	ctx := context.Background()
	unknownKey := int64(999)

	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{APIKeyID: &unknownKey})
	require.Error(t, err)
	require.Nil(t, res)
	require.Contains(t, err.Error(), "trajectory export has no rows")
}

func TestBuildBlob_IncludesInternalThinkingBlocks(t *testing.T) {
	svc := &Service{bodyMaxBytes: 256}
	input := CaptureInput{
		RequestID:                  "req-thinking-1",
		InboundEndpoint:            "/v1/messages",
		StatusCode:                 200,
		CreatedAt:                  time.Now().UTC(),
		RequestBody:                []byte(`{"model":"claude-sonnet-4-6"}`),
		ResponseBody:               []byte(`{"type":"message","content":[{"type":"text","text":"ok"}]}`),
		InternalThinkingBlocksJSON: []string{`{"type":"thinking","signature":"abc","thinking":"internal chain"}`},
	}

	compressed, _, _, _, err := svc.buildBlob(input)
	require.NoError(t, err)

	dec, err := zstd.NewReader(nil)
	require.NoError(t, err)
	raw, err := dec.DecodeAll(compressed, nil)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))

	resp, ok := payload["response"].(map[string]any)
	require.True(t, ok)
	blocks, ok := resp["internal_thinking_blocks"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	blockText, ok := blocks[0].(string)
	require.True(t, ok)
	require.Contains(t, blockText, "signature")
	require.Contains(t, blockText, "thinking")
}
