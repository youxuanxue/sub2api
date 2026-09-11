//go:build unit

package bundle

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sessionBundleFixture(t *testing.T) (*recordingStore, Manifest) {
	t.Helper()
	from := time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)
	evidence := func(request, response string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"evidence": json.RawMessage(`{"request":{"body":` + request + `},"response":{"body":` + response + `},"redactions":["test-v1"]}`)}
	}
	records := []Record{
		{RequestID: "req-claude-1", UserID: 7, APIKeyID: 42, Platform: "anthropic", InboundEndpoint: "/v1/messages", RequestedModel: "claude-sonnet-4-6", Success: true, StatusCode: 200, CapturedAt: from.Add(time.Minute),
			Detail: evidence(`{"messages":[{"role":"user","content":"first QA prompt"}]}`, `{"id":"m1","content":[{"type":"thinking","thinking":"","signature":"QA_SIGNATURE"},{"type":"tool_use","id":"call-1","name":"shell","input":{"command":"ls"}}],"stop_reason":"tool_use","usage":{"output_tokens":9}}`)},
		{RequestID: "req-claude-2", UserID: 7, APIKeyID: 42, Platform: "anthropic", InboundEndpoint: "/v1/messages", RequestedModel: "claude-sonnet-4-6", Success: true, StatusCode: 200, CapturedAt: from.Add(2 * time.Minute),
			Detail: evidence(`{"messages":[{"role":"user","content":"first QA prompt"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"QA_SIGNATURE"},{"type":"tool_use","id":"call-1","name":"shell","input":{"command":"ls"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"file.txt"},{"type":"text","text":"explain it"}]}]}`, `{"id":"m2","content":[{"type":"text","text":"final QA response"}],"stop_reason":"end_turn"}`)},
	}
	store := &recordingStore{}
	spec := NewBundleJobSpec(7, 42, from.Add(24*time.Hour))
	manifest, err := Publish(context.Background(), store, PublishInput{Prefix: spec.GenerationPrefix, DataFrom: from, DataUntil: from.Add(24 * time.Hour), ArchiveWatermark: from.Add(24 * time.Hour), Records: records, MaxRecordsPerPage: 1})
	require.NoError(t, err)
	return store, manifest
}
func unzipSessionArtifact(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	out := map[string][]byte{}
	for _, file := range reader.File {
		r, err := file.Open()
		require.NoError(t, err)
		out[file.Name], err = io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
	}
	return out
}

func TestUS055_BundleSessionZip(t *testing.T) {
	store, manifest := sessionBundleFixture(t)
	spec := NewBundleJobSpec(7, 42, manifest.ArchiveWatermark)
	job := NewZipJobSpec(spec, manifest.ManifestKey)
	// No rawStore is provided: the worker must only consume the committed pages.
	receipt, err := ExecuteJob(context.Background(), job, nil, store, ExecuteDeps{})
	require.NoError(t, err)
	require.Equal(t, 2, receipt.RecordCount)
	files := unzipSessionArtifact(t, store.objects[job.OutputKey])
	require.Len(t, files, 3)
	var sessions []struct {
		SessionID string           `json:"session_id"`
		Calls     []map[string]any `json:"calls"`
		Turns     []map[string]any `json:"turns"`
	}
	dec := json.NewDecoder(bytes.NewReader(files["sessions.jsonl"]))
	for dec.More() {
		var s struct {
			SessionID string           `json:"session_id"`
			Calls     []map[string]any `json:"calls"`
			Turns     []map[string]any `json:"turns"`
		}
		require.NoError(t, dec.Decode(&s))
		sessions = append(sessions, s)
	}
	require.Len(t, sessions, 1)
	require.Len(t, sessions[0].Calls, 2)
	require.Len(t, sessions[0].Turns, 4)
	require.Contains(t, string(files["sessions.jsonl"]), "QA_SIGNATURE")
	require.Contains(t, string(files["sessions.jsonl"]), `"status":"matched"`)
	require.Contains(t, string(files["sessions.jsonl"]), "explain it")
	var index sessionExportManifest
	require.NoError(t, json.Unmarshal(files["export-manifest.json"], &index))
	require.Equal(t, 1, index.SessionCount)
	require.Equal(t, 2, index.RecordCount)
	require.Equal(t, manifest.Generation, index.Source.Generation)
	require.Equal(t, manifest.Pages, index.Source.Pages)
	var records []Record
	decoder := json.NewDecoder(bytes.NewReader(files["qa-records.jsonl"]))
	for decoder.More() {
		var record Record
		require.NoError(t, decoder.Decode(&record))
		records = append(records, record)
	}
	require.Len(t, records, 2)
	require.Equal(t, "req-claude-1", records[0].RequestID)
	repeat, err := ExecuteJob(context.Background(), job, nil, store, ExecuteDeps{Now: func() time.Time { return receipt.CompletedAt }})
	require.NoError(t, err)
	require.Equal(t, receipt.SHA256, repeat.SHA256)
	if output := os.Getenv("TK_QA_SESSION_E2E_FIXTURE"); output != "" {
		require.NoError(t, os.WriteFile(output, store.objects[job.OutputKey], 0o600))
	}
}

func TestUS055_BundleSessionZipRejectsCorruptPageAndCancellation(t *testing.T) {
	store, manifest := sessionBundleFixture(t)
	key := "exports/failed/export.zip"
	store.objects[manifest.Pages[1].Key] = []byte("corrupt")
	_, err := BuildExportZip(context.Background(), store, manifest.ManifestKey, key)
	require.ErrorContains(t, err, "checksum")
	_, exists := store.objects[key]
	require.False(t, exists)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = BuildExportZip(ctx, store, manifest.ManifestKey, key)
	require.ErrorIs(t, err, context.Canceled)
	_, exists = store.objects[key]
	require.False(t, exists)
}

func TestUS055_ExportVersionIsImmutableAndLegacyJobsRemainReadable(t *testing.T) {
	store, manifest := sessionBundleFixture(t)
	parent := NewBundleJobSpec(7, 42, manifest.ArchiveWatermark)
	current := NewZipJobSpec(parent, manifest.ManifestKey)
	require.Equal(t, ExportVersion, current.ExportVersion)
	require.NoError(t, current.Validate())
	legacy := current
	legacy.ExportVersion = ""
	legacy.JobID = deterministicJobID(JobKindBundleZip, 7, 42, parent.ArchiveWatermark, parent.JobID)
	legacy.setDerivedControlKeys()
	legacy.OutputKey = jobBase(legacy.JobID) + "/export.zip"
	require.NoError(t, legacy.Validate())
	require.NotEqual(t, legacy.JobID, current.JobID)
	_, err := ExecuteJob(context.Background(), legacy, nil, store, ExecuteDeps{})
	require.NoError(t, err)
	require.Len(t, unzipSessionArtifact(t, store.objects[legacy.OutputKey]), 1)
	_, err = ExecuteJob(context.Background(), current, nil, store, ExecuteDeps{})
	require.NoError(t, err)
	require.Len(t, unzipSessionArtifact(t, store.objects[current.OutputKey]), 3)
	tampered := current
	tampered.ExportVersion = "unknown"
	require.Error(t, tampered.Validate())
	tampered = current
	tampered.ExportVersion = ""
	require.Error(t, tampered.Validate())
}
