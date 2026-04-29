//go:build unit

package qa

// Issue #59 — verify the gap-1 (HTTP-exposed export) and gap-2 (synth_*
// fields) end-to-end at the service layer, with a real ent client backed
// by in-memory SQLite. Handler-level coverage lives in
// handler/qa_handler_test.go; this file pins the service contract:
//
//   - row-level isolation: query is always WHERE user_id = caller's id
//   - synth_session_id filter overrides the time window
//   - synth_role narrows further when set
//   - returned ExportResult.RecordCount matches the set actually exported
//   - the produced zip blob contains a valid qa_records.jsonl with the
//     ent-encoded fields (so M0's verify_c2_keys.py can read api_key_id
//     and verify_c3_model.py can read upstream_model directly)

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
		SetPlatform("anthropic").
		SetCreatedAt(b.createdAt).
		SetRetentionUntil(b.createdAt.Add(7 * 24 * time.Hour))
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
}

type dlqOnlyBlobStore struct{}

func (dlqOnlyBlobStore) Put(_ context.Context, _ string, _ []byte, _ string) (string, error) {
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
		client:        client,
		store:         dlqOnlyBlobStore{},
		cfg:           config.QACaptureConfig{Enabled: true},
		retentionDays: 7,
		dlqDir:        filepath.Join(t.TempDir(), "qa_dlq"),
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

// ----- US-059 AC-001: row-level ownership is enforced. ------------------

func TestUS059_ExportUserData_OnlyOwnRecords(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Two users, three records. user 7 owns 2, user 8 owns 1.
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "r1", userID: 7, apiKeyID: 1, createdAt: now.Add(-1 * time.Hour)})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "r2", userID: 7, apiKeyID: 1, createdAt: now.Add(-30 * time.Minute)})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "r3", userID: 8, apiKeyID: 2, createdAt: now.Add(-30 * time.Minute)})

	res, err := svc.ExportUserData(ctx, 7, ExportFilter{Since: now.Add(-2 * time.Hour), Until: now})
	require.NoError(t, err)
	require.Equal(t, 2, res.RecordCount, "user 7 should see exactly its own 2 records")
	require.NotEmpty(t, res.DownloadURL)
	require.False(t, res.ExpiresAt.IsZero())
}

// ----- US-059 AC-002: synth_session_id filter narrows + overrides window.

func TestUS059_ExportUserData_BySynthSessionID(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	farPast := now.Add(-72 * time.Hour) // OUTSIDE the default 24h window

	// Two distinct synth sessions for user 7, plus one un-tagged turn.
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "s1-a", userID: 7, apiKeyID: 1, createdAt: farPast, synthSession: "m0-AAA", synthRole: "user-simulator", dialogSynth: true})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "s1-b", userID: 7, apiKeyID: 1, createdAt: farPast.Add(1 * time.Second), synthSession: "m0-AAA", synthRole: "assistant-worker", dialogSynth: true})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "s2-a", userID: 7, apiKeyID: 1, createdAt: farPast, synthSession: "m0-BBB", synthRole: "user-simulator", dialogSynth: true})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "non", userID: 7, apiKeyID: 1, createdAt: now.Add(-1 * time.Hour)})

	// Session filter without time bounds — must still find m0-AAA records
	// even though they're 72h old.
	res, err := svc.ExportUserData(ctx, 7, ExportFilter{SynthSessionID: "m0-AAA"})
	require.NoError(t, err)
	require.Equal(t, 2, res.RecordCount, "session filter must not be capped by time window")

	// Verify the produced zip contains qa_records.jsonl with both
	// records, request_ids in chronological order. We resolve the key
	// from the URL — the in-memory store keys by the same slug.
	require.NotEmpty(t, store.objects)
	var blob []byte
	for _, v := range store.objects {
		blob = v
	}
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	require.NoError(t, err)
	require.Len(t, zr.File, 1)
	require.Equal(t, "qa_records.jsonl", zr.File[0].Name)

	rc, err := zr.File[0].Open()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	require.Len(t, lines, 2)
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Equal(t, "s1-a", first["request_id"], "rows must be ASC by created_at")
	require.Equal(t, "m0-AAA", first["synth_session_id"], "synth_session_id must be present in the exported jsonl (so M0 verify_c2_keys.py can read it)")
	require.Equal(t, "user-simulator", first["synth_role"])
	for _, field := range []string{"api_key_id", "upstream_model", "input_tokens", "output_tokens", "synth_session_id"} {
		require.Contains(t, first, field, "M0 D6 requires exported qa_records.jsonl to keep ent snake_case JSON fields")
	}
	require.Nil(t, first["upstream_model"], "nil optional fields must remain present as JSON null")
}

// ----- US-059 AC-003: synth_role narrows further when both set. ----------

func TestUS059_ExportUserData_RoleNarrows(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "u", userID: 7, apiKeyID: 1, createdAt: now, synthSession: "m0-X", synthRole: "user-simulator", dialogSynth: true})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "a", userID: 7, apiKeyID: 1, createdAt: now, synthSession: "m0-X", synthRole: "assistant-worker", dialogSynth: true})

	res, err := svc.ExportUserData(ctx, 7, ExportFilter{SynthSessionID: "m0-X", SynthRole: "user-simulator"})
	require.NoError(t, err)
	require.Equal(t, 1, res.RecordCount)
}

// ----- US-059 AC-004: time-window-only path (GDPR-style "give me my last
// N hours of data") still works alongside the synth filter — the same
// method, no separate code path. ---------------------------------------

func TestUS059_ExportUserData_TimeWindowOnly(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "in", userID: 7, apiKeyID: 1, createdAt: now.Add(-1 * time.Hour)})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "out-old", userID: 7, apiKeyID: 1, createdAt: now.Add(-48 * time.Hour)})

	res, err := svc.ExportUserData(ctx, 7, ExportFilter{Since: now.Add(-24 * time.Hour), Until: now})
	require.NoError(t, err)
	require.Equal(t, 1, res.RecordCount, "only the in-window record should be exported")
}

// ----- US-059 AC-005 (negative): no matching session → empty export, not
// an error. The M0 client uses RecordCount==0 as the "session not yet
// captured, retry" signal; surfacing an error here would block the
// retry loop. ------------------------------------------------------------

func TestUS059_ExportUserData_UnknownSession_EmptyNotError(t *testing.T) {
	svc, _, _ := newQAExportTestService(t)
	ctx := context.Background()

	res, err := svc.ExportUserData(ctx, 7, ExportFilter{SynthSessionID: "m0-NEVER"})
	require.NoError(t, err)
	require.Equal(t, 0, res.RecordCount)
	require.NotEmpty(t, res.DownloadURL, "even an empty export gets a download URL (zip with empty jsonl)")
}

func TestUS070_PersistCapture_WritesUpstreamModel(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	const (
		sentinelInputTokens  = 123
		sentinelOutputTokens = 45
		sentinelCachedTokens = 6
	)

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
		CreatedAt:    time.Now().UTC(),
	})
	require.NoError(t, err)

	record, err := client.QARecord.Query().Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, record.UpstreamModel)
	require.Equal(t, "claude-sonnet-4-5-20250929", *record.UpstreamModel)
	require.Equal(t, sentinelInputTokens, record.InputTokens)
	require.Equal(t, sentinelOutputTokens, record.OutputTokens)
	require.Equal(t, sentinelCachedTokens, record.CachedTokens)
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

func TestUS074_ExportUserData_FillsDefaultValuedFields(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "defaults", userID: 7, apiKeyID: 1, createdAt: now})

	_, err := svc.ExportUserData(ctx, 7, ExportFilter{Since: now.Add(-1 * time.Hour), Until: now})
	require.NoError(t, err)

	var blob []byte
	for _, v := range store.objects {
		blob = v
	}
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	require.NoError(t, err)
	rc, err := zr.File[0].Open()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)

	var row map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(raw), &row))
	require.Equal(t, float64(0), row["cached_tokens"])
	require.Equal(t, false, row["tool_calls_present"])
	require.Equal(t, false, row["multimodal_present"])
	require.Equal(t, []any{}, row["tags"])
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
		SetPlatform("anthropic").
		SetCreatedAt(b.createdAt).
		SetRetentionUntil(b.createdAt.Add(7 * 24 * time.Hour)).
		SetBlobURI("mem://" + key)
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

	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{SynthSessionID: "m0-TRAJ"})
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

func runTrajectoryDatasetCheck(t *testing.T, datasetPath string, args ...string) (int, string) {
	t.Helper()
	commandArgs := append([]string{"scripts/check-traj-dataset.py", datasetPath}, args...)
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

func TestUS077_TrajectoryDatasetCheck_AcceptsProjectedExportZip(t *testing.T) {
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

	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{SynthSessionID: "m0-GATE"})
	require.NoError(t, err)
	zipPath := writeTrajectoryExportFixture(t, store.objects[res.StorageKey])

	exitCode, output := runTrajectoryDatasetCheck(t, zipPath)
	require.Equal(t, 0, exitCode, output)
	require.Contains(t, output, "ok: trajectory dataset passed H1/H2/H3/D1 and structural checks")
}

func TestUS077_TrajectoryDatasetCheck_RejectsDuplicateTurnDataset(t *testing.T) {
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

	exitCode, output := runTrajectoryDatasetCheck(t, datasetPath)
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

	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{SynthSessionID: "m0-DOWNLOAD"})
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

func TestUS077_ExportUserTrajectoryData_UnknownSession_EmptyFails(t *testing.T) {
	svc, _, _ := newQAExportTestService(t)
	ctx := context.Background()

	res, err := svc.ExportUserTrajectoryData(ctx, 7, ExportFilter{SynthSessionID: "m0-NEVER"})
	require.Error(t, err)
	require.Nil(t, res)
	require.Contains(t, err.Error(), "trajectory export has no rows")
}

func TestUS033_DownloadUserExport_OwnedKeyOnly(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "owned", userID: 7, apiKeyID: 1, createdAt: now})

	res, err := svc.ExportUserData(ctx, 7, ExportFilter{Since: now.Add(-1 * time.Hour), Until: now})
	require.NoError(t, err)
	require.NotEmpty(t, res.StorageKey)

	body, err := svc.DownloadUserExport(ctx, 7, res.StorageKey)
	require.NoError(t, err)
	require.Equal(t, store.objects[res.StorageKey], body)

	_, err = svc.DownloadUserExport(ctx, 8, res.StorageKey)
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrPermission))

	_, err = svc.DownloadUserExport(ctx, 7, "../7/"+res.StorageKey)
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrPermission))

	expiredKey := "exports/7/1.zip"
	store.objects[expiredKey] = []byte("expired")
	_, err = svc.DownloadUserExport(ctx, 7, expiredKey)
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrNotExist))
}
