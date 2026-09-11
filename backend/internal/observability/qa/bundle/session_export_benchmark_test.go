//go:build unit

package bundle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type exportCostStore struct {
	*recordingStore
	start    time.Time
	cpuStart float64
	result   exportCostResult
}

type exportCostResult struct {
	cpu, wall                      float64
	zip, logical, allocated, files int64
}

func exportCPU() float64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		panic(err)
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}
func (s *exportCostStore) Create(ctx context.Context, key string, body io.Reader, size int64, meta ObjectMetadata) error {
	if !strings.HasSuffix(key, ".zip") {
		return s.recordingStore.Create(ctx, key, body, size, meta)
	}
	// Capture computation cost before disk accounting or simulated upload.
	wall := time.Since(s.start).Seconds()
	cpu := exportCPU() - s.cpuStart
	var logical, allocated, files int64
	file, ok := body.(*os.File)
	if !ok {
		return fmt.Errorf("expected ZIP file, got %T", body)
	}
	err := filepath.Walk(filepath.Dir(file.Name()), func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			logical += info.Size()
			files++
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return fmt.Errorf("expected filesystem block statistics")
			}
			allocated += stat.Blocks * 512
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.result = exportCostResult{wall: wall, cpu: cpu, zip: size, logical: logical, allocated: allocated, files: files}
	_, err = io.Copy(io.Discard, body)
	return err
}
func exportCostText(r *rand.Rand, words int) string {
	vocabulary := strings.Fields("request response gateway session model tool argument result user assistant message record field service account capture export stream context sequence value function source status timeout process document runtime state output input token usage validate error retry item metadata client server connection storage data version complete history signature reasoning content parameter cache boundary event example implementation file test code memory disk compute information system analyze generate return operation application endpoint format object payload array string number timestamp project task worker queue archive page bundle")
	var b strings.Builder
	for n := 0; n < words; n++ {
		if n > 0 {
			_ = b.WriteByte(' ')
		}
		_, _ = b.WriteString(vocabulary[r.Intn(len(vocabulary))])
	}
	return b.String()
}
func BenchmarkSessionExport(b *testing.B) {
	ctx := context.Background()
	for _, c := range []struct {
		name            string
		sessions, turns int
		stream          bool
	}{
		{"single_turn", 300, 1, false}, {"ten_turn", 30, 10, false}, {"fifty_turn", 6, 50, false}, {"ten_turn_sse", 30, 10, true}, {"spill_single", 1200, 1, false}, {"spill_ten", 120, 10, false},
	} {
		r := rand.New(rand.NewSource(1752))
		from := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
		records := make([]Record, 0, c.sessions*c.turns)
		for sid := 0; sid < c.sessions; sid++ {
			history := []any{}
			for turn := 0; turn < c.turns; turn++ {
				history = append(history, map[string]any{"role": "user", "content": exportCostText(r, 300)})
				// Native text plus occasional tool call/result exercises index writes.
				content := []map[string]any{{"type": "text", "text": exportCostText(r, 600)}}
				if turn%3 == 0 {
					content = append(content, map[string]any{"type": "tool_use", "id": fmt.Sprintf("tool-%d-%d", sid, turn), "name": "read_file", "input": map[string]any{"path": fmt.Sprintf("source/%d.go", turn)}})
				}
				response := map[string]any{"id": fmt.Sprintf("msg-%d-%d", sid, turn), "type": "message", "role": "assistant", "content": content, "stop_reason": "end_turn", "usage": map[string]any{"output_tokens": 800}}
				evidence := map[string]any{"request": map[string]any{"body": map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 4096, "messages": history}}, "response": map[string]any{"body": response}}
				if c.stream {
					chunks := []any{}
					event := func(name string, value any) {
						b, _ := json.Marshal(value)
						chunks = append(chunks, map[string]string{"raw_b64": base64.StdEncoding.EncodeToString([]byte("event: " + name + "\ndata: " + string(b) + "\n\n"))})
					}
					event("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": response["id"], "type": "message", "role": "assistant", "content": []any{}}})
					for index, block := range content {
						if m := block; m["type"] == "text" {
							event("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}})
							value, ok := m["text"].(string)
							if !ok {
								b.Fatal("fixture text must be a string")
							}
							for n := 0; n < len(value); n += 100 {
								end := n + 100
								if end > len(value) {
									end = len(value)
								}
								event("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": value[n:end]}})
							}
						} else {
							event("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": block})
						}
						event("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
					}
					event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": response["usage"]})
					event("message_stop", map[string]any{"type": "message_stop"})
					evidence["response"] = map[string]any{}
					evidence["stream"] = map[string]any{"chunks": chunks}
				}
				raw, err := json.Marshal(evidence)
				if err != nil {
					b.Fatal(err)
				}
				records = append(records, Record{RequestID: fmt.Sprintf("req-%d-%d", sid, turn), UserID: 7, APIKeyID: 42, Platform: "anthropic", InboundEndpoint: "/v1/messages", CapturedAt: from.Add(time.Duration(len(records)) * time.Second), StatusCode: 200, Success: true, Stream: c.stream, Detail: map[string]json.RawMessage{"evidence": raw}})
				history = append(history, map[string]any{"role": "assistant", "content": content})
				if turn%3 == 0 {
					history = append(history, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("tool-%d-%d", sid, turn), "content": exportCostText(r, 300)}}})
				}
			}
		}
		store := &recordingStore{}
		manifest, err := Publish(ctx, store, PublishInput{Prefix: "cost/" + c.name, DataFrom: from, DataUntil: from.Add(24 * time.Hour), ArchiveWatermark: from.Add(24 * time.Hour), Records: records})
		if err != nil {
			b.Fatal(err)
		}
		for _, version := range []string{"", ExportVersion} {
			name := version
			if name == "" {
				name = "raw"
			}
			b.Run(c.name+"/"+name, func(b *testing.B) {
				var cpu, wall float64
				var last exportCostResult
				for n := 0; n < b.N; n++ {
					observed := &exportCostStore{recordingStore: store}
					observed.cpuStart = exportCPU()
					observed.start = time.Now()
					if _, err := buildExportZip(ctx, observed, manifest.ManifestKey, "cost/export.zip", version); err != nil {
						b.Fatal(err)
					}
					last = observed.result
					cpu += last.cpu
					wall += last.wall
				}
				b.ReportMetric(cpu/float64(b.N)*1e9, "cpu-ns/export")
				b.ReportMetric(wall/float64(b.N)*1e9, "build-ns/export")
				b.ReportMetric(float64(last.zip), "zip_bytes/export")
				b.ReportMetric(float64(last.allocated), "temp_allocated_bytes/export")
				b.ReportMetric(float64(last.files), "temp_files/export")
			})
		}
	}
}
