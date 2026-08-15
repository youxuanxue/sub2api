//go:build unit

package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

func TestProjectVerifiedSegmentsFiltersScopeAndRestoresFullDetail(t *testing.T) {
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

	projected, err := ProjectVerifiedSegments([]archive.VerifiedSegment{{RestoreDir: restoreDir}}, 7, 11)
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

func TestProjectVerifiedSegmentsRejectsCorruptEvidence(t *testing.T) {
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

	if _, err := ProjectVerifiedSegments([]archive.VerifiedSegment{{RestoreDir: restoreDir}}, 7, 11); err == nil {
		t.Fatal("ProjectVerifiedSegments() accepted corrupt evidence")
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
