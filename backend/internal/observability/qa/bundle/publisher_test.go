//go:build unit

package bundle

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type recordingStore struct {
	objects map[string][]byte
	meta    map[string]ObjectMetadata
	writes  []string
	opens   []string
	reads   []string
	readers map[string]string
	failKey string
}

func (s *recordingStore) Create(_ context.Context, key string, body io.Reader, size int64, meta ObjectMetadata) error {
	if key == s.failKey {
		return errors.New("injected create failure")
	}
	if s.objects == nil {
		s.objects = map[string][]byte{}
		s.meta = map[string]ObjectMetadata{}
		s.readers = map[string]string{}
	}
	if _, exists := s.objects[key]; exists {
		return ErrObjectExists
	}
	payload, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(payload)) != size {
		return errors.New("size mismatch")
	}
	s.objects[key] = payload
	s.meta[key] = meta
	s.readers[key] = fmt.Sprintf("%T", body)
	s.writes = append(s.writes, key)
	return nil
}

func (s *recordingStore) Read(_ context.Context, key string) ([]byte, error) {
	s.reads = append(s.reads, key)
	payload, ok := s.objects[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), payload...), nil
}

func (s *recordingStore) Open(_ context.Context, key string) (ObjectReader, error) {
	payload, ok := s.objects[key]
	if !ok {
		return ObjectReader{}, errors.New("not found")
	}
	s.opens = append(s.opens, key)
	return ObjectReader{Body: io.NopCloser(bytes.NewReader(payload)), Size: int64(len(payload))}, nil
}

func (s *recordingStore) Head(_ context.Context, key string) (bool, error) {
	_, ok := s.objects[key]
	return ok, nil
}

func TestPublishCommitsFullDetailPagesBeforeManifest(t *testing.T) {
	store := &recordingStore{}
	from := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	records := []Record{
		{
			RequestID: "req-1", UserID: 7, APIKeyID: 11, Platform: "anthropic",
			RequestedModel: "claude-sonnet-4", StatusCode: 200, Success: true,
			CapturedAt: from.Add(time.Minute),
			Detail: map[string]json.RawMessage{
				"request":  json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`),
				"response": json.RawMessage(`{"content":[{"type":"text","text":"world"}]}`),
			},
		},
		{RequestID: "req-2", UserID: 7, APIKeyID: 11, Platform: "anthropic", StatusCode: 500, CapturedAt: from.Add(2 * time.Minute)},
	}

	manifest, err := Publish(context.Background(), store, PublishInput{
		Prefix:                 "bundles/7/11/generations/g-1",
		DataFrom:               from,
		DataUntil:              from.Add(24 * time.Hour),
		ArchiveWatermark:       from.Add(24 * time.Hour),
		Records:                records,
		MaxRecordsPerPage:      1,
		MaxCompressedPageBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RecordCount != 2 || len(manifest.Pages) != 2 {
		t.Fatalf("manifest=%+v", manifest)
	}
	if got := store.writes[len(store.writes)-1]; got != "bundles/7/11/generations/g-1/manifest.json" {
		t.Fatalf("manifest was not the final publish marker: writes=%v", store.writes)
	}
	for _, key := range store.writes {
		if strings.Contains(key, "/records/") {
			t.Fatalf("per-record object was published: %s", key)
		}
	}
	pageKey := manifest.Pages[0].Key
	if meta := store.meta[pageKey]; meta.ContentType != "application/json" || meta.ContentEncoding != "gzip" {
		t.Fatalf("page metadata=%+v", meta)
	}
	zr, err := gzip.NewReader(bytes.NewReader(store.objects[pageKey]))
	if err != nil {
		t.Fatal(err)
	}
	pageBody, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
	}
	var page Page
	if err := json.Unmarshal(pageBody, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].RequestID != "req-1" || len(page.Records[0].Detail) != 2 {
		t.Fatalf("page=%+v", page)
	}
}

func TestPublishFailureLeavesPartialGenerationInvisible(t *testing.T) {
	prefix := "bundles/7/11/generations/g-failed"
	store := &recordingStore{failKey: prefix + "/manifest.json"}
	from := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	_, err := Publish(context.Background(), store, PublishInput{
		Prefix:                 prefix,
		DataFrom:               from,
		DataUntil:              from.Add(24 * time.Hour),
		ArchiveWatermark:       from.Add(24 * time.Hour),
		Records:                []Record{{RequestID: "req-1", UserID: 7, APIKeyID: 11, CapturedAt: from.Add(time.Minute)}},
		MaxRecordsPerPage:      100,
		MaxCompressedPageBytes: 1 << 20,
	})
	if err == nil {
		t.Fatal("Publish() unexpectedly succeeded")
	}
	if _, visible := store.objects[prefix+"/manifest.json"]; visible {
		t.Fatal("failed generation published a manifest")
	}
	if len(store.objects) == 0 {
		t.Fatal("test did not exercise a partial page upload")
	}
}

func TestBuildExportZipReadsOnlyCommittedBundlePages(t *testing.T) {
	store := &recordingStore{}
	from := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	manifest, err := Publish(context.Background(), store, PublishInput{
		Prefix:                 "bundles/7/11/generations/g-export",
		DataFrom:               from,
		DataUntil:              from.Add(24 * time.Hour),
		ArchiveWatermark:       from.Add(24 * time.Hour),
		Records:                []Record{{RequestID: "req-export", UserID: 7, APIKeyID: 11, CapturedAt: from.Add(time.Minute)}},
		MaxRecordsPerPage:      100,
		MaxCompressedPageBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	zipKey := "exports/e-1/export.zip"
	receipt, err := BuildExportZip(context.Background(), store, manifest.ManifestKey, zipKey)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RecordCount != 1 || receipt.StorageKey != zipKey {
		t.Fatalf("receipt=%+v", receipt)
	}
	if got := store.readers[zipKey]; got != "*os.File" {
		t.Fatalf("zip upload reader=%s, want file-backed streaming", got)
	}
	zipped := store.objects[zipKey]
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "qa-records.jsonl" {
		t.Fatalf("zip files=%v", zr.File)
	}
	file, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if !bytes.Contains(body, []byte(`"request_id":"req-export"`)) {
		t.Fatalf("zip body=%s", body)
	}

	repeated, err := BuildExportZip(context.Background(), store, manifest.ManifestKey, zipKey)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.SHA256 != receipt.SHA256 || len(store.opens) == 0 || store.opens[len(store.opens)-1] != zipKey {
		t.Fatalf("repeated=%+v opens=%v", repeated, store.opens)
	}
	for _, key := range store.reads {
		if key == zipKey {
			t.Fatalf("existing ZIP was read into memory: reads=%v", store.reads)
		}
	}
}
