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
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/klauspost/compress/zstd"
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

	// Self-contained session export requires persisted blobs (issue #79).
	msgUser := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"ping"}],"tools":[{"name":"demo_tool","description":"d","input_schema":{"type":"object","properties":{}}}]}`

	err := svc.persistCapture(ctx, CaptureInput{
		RequestID: "s1-a", UserID: 7, APIKeyID: 1, Platform: "anthropic",
		StatusCode: 200, InboundEndpoint: "/v1/messages",
		RequestBody:    []byte(msgUser),
		ResponseBody:   []byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"demo_tool","input":{}}]}`),
		CreatedAt:      farPast,
		SynthSessionID: "m0-AAA", SynthRole: "user-simulator", DialogSynth: true,
	})
	require.NoError(t, err)
	err = svc.persistCapture(ctx, CaptureInput{
		RequestID: "s1-b", UserID: 7, APIKeyID: 1, Platform: "anthropic",
		StatusCode: 200, InboundEndpoint: "/v1/messages",
		RequestBody: []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]}]}`),
		ResponseBody:   []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}]}`),
		CreatedAt:      farPast.Add(1 * time.Second),
		SynthSessionID: "m0-AAA", SynthRole: "assistant-worker", DialogSynth: true,
	})
	require.NoError(t, err)
	// Other sessions / untagged traffic must not appear in m0-AAA export.
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "s2-a", userID: 7, apiKeyID: 1, createdAt: farPast, synthSession: "m0-BBB", synthRole: "user-simulator", dialogSynth: true})
	mustInsertQARecord(t, ctx, client, qaRecordBuilder{requestID: "non", userID: 7, apiKeyID: 1, createdAt: now.Add(-1 * time.Hour)})

	// Session filter without time bounds — must still find m0-AAA records
	// even though they're 72h old.
	res, err := svc.ExportUserData(ctx, 7, ExportFilter{SynthSessionID: "m0-AAA"})
	require.NoError(t, err)
	require.Equal(t, 2, res.RecordCount, "session filter must not be capped by time window")
	require.Equal(t, QAExportFormatVersion, res.ExportFormatVersion)
	require.False(t, res.ExportIncomplete)
	require.NotEmpty(t, res.StorageKey)

	zipBytes := store.objects[res.StorageKey]
	require.NotEmpty(t, zipBytes, "export zip must be written to blob store")

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	require.Contains(t, names, "qa_records.jsonl")
	require.Contains(t, names, "manifest.json")
	require.Contains(t, names, "blobs/s1-a.json.zst")
	require.Contains(t, names, "blobs/s1-b.json.zst")

	manifestRaw := readZipFile(t, zr, "manifest.json")
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(manifestRaw, &manifest))
	require.Equal(t, QAExportFormatVersion, manifest["export_format_version"])
	require.Equal(t, true, manifest["includes_blobs"])
	require.Equal(t, float64(2), manifest["record_count"])

	jsonl := readZipFile(t, zr, "qa_records.jsonl")
	lines := strings.Split(strings.TrimRight(string(jsonl), "\n"), "\n")
	require.Len(t, lines, 2)
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Equal(t, "s1-a", first["request_id"], "rows must be ASC by created_at + request_id")
	require.Equal(t, "m0-AAA", first["synth_session_id"], "synth_session_id must be present in the exported jsonl (so M0 verify_c2_keys.py can read it)")
	require.Equal(t, "user-simulator", first["synth_role"])
	require.Equal(t, "blobs/s1-a.json.zst", first["capture_archive_path"])
	_, hasBlob := first["blob_uri"]
	require.False(t, hasBlob, "self-contained export must not rely on blob_uri in jsonl")
	for _, field := range []string{"api_key_id", "upstream_model", "input_tokens", "output_tokens", "synth_session_id"} {
		require.Contains(t, first, field, "M0 D6 requires exported qa_records.jsonl to keep ent snake_case JSON fields")
	}
	require.Nil(t, first["upstream_model"], "nil optional fields must remain present as JSON null")

	zstdBlob := readZipFile(t, zr, "blobs/s1-a.json.zst")
	dec, err := zstd.NewReader(nil)
	require.NoError(t, err)
	defer dec.Close()
	raw, err := dec.DecodeAll(zstdBlob, nil)
	require.NoError(t, err)
	var capPayload map[string]any
	require.NoError(t, json.Unmarshal(raw, &capPayload))
	req := capPayload["request"].(map[string]any)
	body := req["body"].(map[string]any)
	msgs := body["messages"].([]any)
	require.NotEmpty(t, msgs)
}

func readZipFile(t *testing.T, zr *zip.Reader, name string) []byte {
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

// ----- US-059 AC-003: synth_role narrows further when both set. ----------

func TestUS059_ExportUserData_RoleNarrows(t *testing.T) {
	svc, _, _ := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	err := svc.persistCapture(ctx, CaptureInput{
		RequestID: "u", UserID: 7, APIKeyID: 1, Platform: "anthropic",
		StatusCode: 200, RequestBody: []byte(`{"model":"m"}`), ResponseBody: []byte(`{}`),
		CreatedAt: now, SynthSessionID: "m0-X", SynthRole: "user-simulator", DialogSynth: true,
	})
	require.NoError(t, err)
	err = svc.persistCapture(ctx, CaptureInput{
		RequestID: "a", UserID: 7, APIKeyID: 1, Platform: "anthropic",
		StatusCode: 200, RequestBody: []byte(`{"model":"m"}`), ResponseBody: []byte(`{}`),
		CreatedAt: now, SynthSessionID: "m0-X", SynthRole: "assistant-worker", DialogSynth: true,
	})
	require.NoError(t, err)

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

// Issue #79 — at least three QA rows in one synth_session_id; zip alone must
// yield decodable Messages-style JSON from each blob (tool_use / tool_result chain).
func TestUS079_ExportSynthSession_SelfContainedThreeTurns(t *testing.T) {
	svc, _, store := newQAExportTestService(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-10 * time.Minute)
	session := "synth-chain-79"

	toolUseResp := `{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"x"}}]}`
	toolResultReq := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"found"}]}]}`
	finalResp := `{"type":"message","role":"assistant","content":[{"type":"text","text":"thanks"}]}`

	for i, rid := range []string{"turn-1", "turn-2", "turn-3"} {
		var reqBody, respBody []byte
		switch i {
		case 0:
			reqBody = []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"go"}],"tools":[{"name":"lookup","description":"d","input_schema":{"type":"object","properties":{}}}]}`)
			respBody = []byte(toolUseResp)
		case 1:
			reqBody = []byte(toolResultReq)
			respBody = []byte(`{"type":"message","role":"assistant","content":[]}`)
		case 2:
			reqBody = []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"wrap"}]}`)
			respBody = []byte(finalResp)
		}
		err := svc.persistCapture(ctx, CaptureInput{
			RequestID: rid, UserID: 7, APIKeyID: 1, Platform: "anthropic",
			StatusCode: 200, InboundEndpoint: "/v1/messages",
			RequestBody: reqBody, ResponseBody: respBody,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
			SynthSessionID: session, SynthRole: "cli", DialogSynth: true,
		})
		require.NoError(t, err)
	}

	res, err := svc.ExportUserData(ctx, 7, ExportFilter{SynthSessionID: session})
	require.NoError(t, err)
	require.Equal(t, 3, res.RecordCount)
	require.False(t, res.ExportIncomplete)

	zipBytes := store.objects[res.StorageKey]
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)

	for _, rid := range []string{"turn-1", "turn-2", "turn-3"} {
		zpath := "blobs/" + rid + ".json.zst"
		rawZ := readZipFile(t, zr, zpath)
		dec, err := zstd.NewReader(nil)
		require.NoError(t, err)
		payload, err := dec.DecodeAll(rawZ, nil)
		dec.Close()
		require.NoError(t, err)
		var root map[string]any
		require.NoError(t, json.Unmarshal(payload, &root))
		req := root["request"].(map[string]any)
		body := req["body"].(map[string]any)
		require.Contains(t, body, "messages")
	}

	lines := strings.Split(strings.TrimSpace(string(readZipFile(t, zr, "qa_records.jsonl"))), "\n")
	require.Len(t, lines, 3)
}

func TestUS079_ExportSynthSession_BodyTruncatedFails(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	session := "synth-trunc-79"

	_, err := client.QARecord.Create().
		SetRequestID("bad-trunc").
		SetUserID(7).
		SetAPIKeyID(1).
		SetPlatform("anthropic").
		SetSynthSessionID(session).
		SetSynthRole("cli").
		SetDialogSynth(true).
		SetTags([]string{"body_truncated"}).
		SetCreatedAt(now).
		SetRetentionUntil(now.Add(24 * time.Hour)).
		SetBlobURI("mem://2026/01/01/ba/bad-trunc.json.zst").
		Save(ctx)
	require.NoError(t, err)

	_, err = svc.ExportUserData(ctx, 7, ExportFilter{SynthSessionID: session})
	require.Error(t, err)
	require.Contains(t, err.Error(), "body_truncated")
}

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
	raw := readZipFile(t, zr, "qa_records.jsonl")

	var row map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(raw), &row))
	require.Equal(t, float64(0), row["cached_tokens"])
	require.Equal(t, false, row["tool_calls_present"])
	require.Equal(t, false, row["multimodal_present"])
	require.Equal(t, []any{}, row["tags"])
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
