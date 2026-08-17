//go:build unit

package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

func TestVisitVerifiedSegmentsFiltersScopeAndRestoresFullDetail(t *testing.T) {
	restoreDir := t.TempDir()
	from := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	channelType := int64(5)
	inbound := "/v1/messages"
	firstToken := int64(123)
	toolCalls := true
	cached := int64(7)
	tags := `["training"]`
	dialogSynth := true
	rows := []archive.RecordRow{
		{
			RequestID: "req-owned", UserID: 7, APIKeyID: 11, Platform: "anthropic",
			RequestedModel: "claude-sonnet-4", StatusCode: 200, Success: true,
			DurationMS: 900, InputTokens: 12, OutputTokens: 34, CaptureStatus: "captured",
			CreatedAt: from.Add(time.Minute).UnixMicro(), ChannelType: &channelType,
			InboundEndpoint: &inbound, FirstTokenMS: &firstToken, ToolCallsPresent: &toolCalls,
			CachedTokens: &cached, TagsJSON: &tags, DialogSynth: &dialogSynth,
		},
		{RequestID: "req-foreign", UserID: 8, APIKeyID: 11, Platform: "anthropic", CreatedAt: from.Add(2 * time.Minute).UnixMicro()},
		{RequestID: "req-other-key", UserID: 7, APIKeyID: 12, Platform: "anthropic", CreatedAt: from.Add(3 * time.Minute).UnixMicro()},
	}
	writeParquetRows(t, filepath.Join(restoreDir, "records.parquet"), rows)
	writeZstdJSON(t, filepath.Join(restoreDir, "evidence", "req-owned", "request_blob_uri.bin"), `{"messages":[{"role":"user","content":"hello"}]}`)
	writeZstdJSON(t, filepath.Join(restoreDir, "evidence", "req-owned", "response_blob_uri.bin"), `{"content":[{"type":"text","text":"world"}]}`)

	var projected []Record
	err := visitVerifiedSegments([]archive.VerifiedSegment{{RestoreDir: restoreDir}}, 7, 11, func(record Record) error {
		projected = append(projected, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 1 {
		t.Fatalf("projected=%+v", projected)
	}
	record := projected[0]
	if record.RequestID != "req-owned" || record.InboundEndpoint != inbound ||
		record.FirstTokenMS == nil || *record.FirstTokenMS != firstToken ||
		record.CachedTokens != int(cached) || len(record.Tags) != 1 || record.Tags[0] != "training" {
		t.Fatalf("record=%+v", record)
	}
	var request map[string]any
	if err := json.Unmarshal(record.Detail["request"], &request); err != nil {
		t.Fatal(err)
	}
	if _, ok := request["messages"]; !ok || len(record.Detail["response"]) == 0 {
		t.Fatalf("detail=%v", record.Detail)
	}
}

func TestVisitVerifiedSegmentsRejectsCorruptEvidence(t *testing.T) {
	restoreDir := t.TempDir()
	from := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	uri := "file://request"
	writeParquetRows(t, filepath.Join(restoreDir, "records.parquet"), []archive.RecordRow{{
		RequestID: "req-corrupt", UserID: 7, APIKeyID: 11, Platform: "anthropic",
		CreatedAt: from.UnixMicro(), RequestBlobURI: &uri,
	}})
	path := filepath.Join(restoreDir, "evidence", "req-corrupt", "request_blob_uri.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-zstd"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := visitVerifiedSegments([]archive.VerifiedSegment{{RestoreDir: restoreDir}}, 7, 11, func(Record) error { return nil }); err == nil {
		t.Fatal("visitVerifiedSegments() accepted corrupt evidence")
	}
}

func TestVisitVerifiedSegmentsMergesDeterministically(t *testing.T) {
	from := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	segments := make([]archive.VerifiedSegment, 0, 2)
	for segmentIndex, offsets := range [][]int{{0, 2, 4}, {1, 3, 5}} {
		restoreDir := t.TempDir()
		rows := make([]archive.RecordRow, 0, len(offsets))
		for _, offset := range offsets {
			rows = append(rows, archive.RecordRow{
				RequestID: fmt.Sprintf("req-%02d", offset), UserID: 7, APIKeyID: 11,
				Platform: "anthropic", CreatedAt: from.Add(time.Duration(offset) * time.Minute).UnixMicro(),
			})
		}
		writeParquetRows(t, filepath.Join(restoreDir, "records.parquet"), rows)
		segments = append(segments, archive.VerifiedSegment{RestoreDir: restoreDir, Manifest: archive.SegmentManifest{SegmentID: fmt.Sprintf("segment-%d", segmentIndex)}})
	}

	var requestIDs []string
	if err := visitVerifiedSegments(segments, 7, 11, func(record Record) error {
		requestIDs = append(requestIDs, record.RequestID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(requestIDs, ","), "req-00,req-01,req-02,req-03,req-04,req-05"; got != want {
		t.Fatalf("request order=%s want=%s", got, want)
	}
}

func TestPublishVerifiedCommitsStreamsPagesBeforeLateProjectionFailure(t *testing.T) {
	restoreDir := t.TempDir()
	from := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	rows := make([]archive.RecordRow, 0, 40)
	for index := 0; index < 40; index++ {
		row := archive.RecordRow{
			RequestID: fmt.Sprintf("req-%03d", index), UserID: 7, APIKeyID: 11,
			Platform: "anthropic", CreatedAt: from.Add(time.Duration(index) * time.Minute).UnixMicro(),
		}
		if index == 25 {
			uri := "file://corrupt-request"
			row.RequestBlobURI = &uri
		}
		rows = append(rows, row)
	}
	writeParquetRows(t, filepath.Join(restoreDir, "records.parquet"), rows)
	corruptPath := filepath.Join(restoreDir, "evidence", "req-025", "request_blob_uri.bin")
	if err := os.MkdirAll(filepath.Dir(corruptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte("not-zstd"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &recordingStore{}
	prefix := "bundles/7/11/generations/g-streaming"
	_, err := PublishVerifiedCommits(context.Background(), store, PublishInput{
		Prefix: prefix, DataFrom: from, DataUntil: from.Add(24 * time.Hour), ArchiveWatermark: from.Add(24 * time.Hour),
		MaxRecordsPerPage: 5, MaxCompressedPageBytes: 1 << 20,
	}, []archive.VerifiedCommit{{Segments: []archive.VerifiedSegment{{RestoreDir: restoreDir}}}}, 7, 11)
	if err == nil {
		t.Fatal("PublishVerifiedCommits() accepted corrupt late evidence")
	}
	if len(store.writes) != 4 {
		t.Fatalf("writes=%v, want four completed bounded pages before the late failure", store.writes)
	}
	if _, visible := store.objects[prefix+"/manifest.json"]; visible {
		t.Fatal("failed streamed projection published a manifest")
	}
}

func writeParquetRows(t *testing.T, path string, rows []archive.RecordRow) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := parquet.NewGenericWriter[archive.RecordRow](file)
	if _, err := writer.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZstdJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
