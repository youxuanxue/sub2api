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
	"io"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
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
	svc := NewServiceForTest(client, store, 16*1024, 7)
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
