package bundle

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion          = "qa-bundle-v1"
	ExportReceiptSchema    = "qa-bundle-export-v1"
	defaultPageRecordLimit = 100
	defaultPageByteLimit   = 4 << 20
	defaultPageMemoryLimit = 64 << 20
)

var ErrObjectExists = errors.New("qa bundle object already exists")

type ObjectMetadata struct {
	ContentType     string
	ContentEncoding string
}

type Store interface {
	Create(context.Context, string, io.Reader, int64, ObjectMetadata) error
	Read(context.Context, string) ([]byte, error)
	Open(context.Context, string) (ObjectReader, error)
	Head(context.Context, string) (bool, error)
}

type ObjectReader struct {
	Body io.ReadCloser
	Size int64
}

type Record struct {
	RequestID          string                     `json:"request_id"`
	TrajectoryID       *string                    `json:"trajectory_id,omitempty"`
	UserID             int64                      `json:"user_id"`
	GroupID            *int64                     `json:"group_id,omitempty"`
	APIKeyID           int64                      `json:"api_key_id"`
	ChannelType        *int64                     `json:"channel_type,omitempty"`
	Platform           string                     `json:"platform"`
	Provider           *string                    `json:"provider,omitempty"`
	RequestedModel     string                     `json:"requested_model"`
	UpstreamModel      *string                    `json:"upstream_model,omitempty"`
	InboundEndpoint    string                     `json:"inbound_endpoint,omitempty"`
	UpstreamEndpoint   *string                    `json:"upstream_endpoint,omitempty"`
	StatusCode         int                        `json:"status_code"`
	Success            bool                       `json:"success"`
	DurationMS         int64                      `json:"duration_ms"`
	FirstTokenMS       *int64                     `json:"first_token_ms,omitempty"`
	Stream             bool                       `json:"stream"`
	InputTokens        int                        `json:"input_tokens"`
	OutputTokens       int                        `json:"output_tokens"`
	CachedTokens       int                        `json:"cached_tokens"`
	ToolCallsPresent   bool                       `json:"tool_calls_present"`
	MultimodalPresent  bool                       `json:"multimodal_present"`
	CaptureStatus      string                     `json:"capture_status,omitempty"`
	RedactionVersion   string                     `json:"redaction_version,omitempty"`
	Tags               []string                   `json:"tags,omitempty"`
	SynthSessionID     *string                    `json:"synth_session_id,omitempty"`
	SynthRole          *string                    `json:"synth_role,omitempty"`
	SynthEngineerLevel *string                    `json:"synth_engineer_level,omitempty"`
	DialogSynth        bool                       `json:"dialog_synth"`
	CapturedAt         time.Time                  `json:"captured_at"`
	Detail             map[string]json.RawMessage `json:"detail,omitempty"`
}

type Page struct {
	SchemaVersion string   `json:"schema_version"`
	Page          int      `json:"page"`
	Records       []Record `json:"records"`
}

type PageDescriptor struct {
	Page            int    `json:"page"`
	Key             string `json:"key"`
	RecordCount     int    `json:"record_count"`
	CompressedBytes int64  `json:"compressed_bytes"`
	SHA256          string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion    string           `json:"schema_version"`
	Generation       string           `json:"generation"`
	DataFrom         time.Time        `json:"data_from"`
	DataUntil        time.Time        `json:"data_until"`
	ArchiveWatermark time.Time        `json:"archive_watermark"`
	RecordCount      int              `json:"record_count"`
	Pages            []PageDescriptor `json:"pages"`
	ManifestKey      string           `json:"-"`
}

type PublishInput struct {
	Prefix                   string
	DataFrom                 time.Time
	DataUntil                time.Time
	ArchiveWatermark         time.Time
	Records                  []Record
	MaxRecordsPerPage        int
	MaxCompressedPageBytes   int
	MaxUncompressedPageBytes int
}

type ExportReceipt struct {
	SchemaVersion string    `json:"schema_version"`
	ManifestKey   string    `json:"manifest_key"`
	StorageKey    string    `json:"storage_key"`
	RecordCount   int       `json:"record_count"`
	SHA256        string    `json:"sha256"`
	CreatedAt     time.Time `json:"created_at"`
}

func Publish(ctx context.Context, store Store, input PublishInput) (Manifest, error) {
	records := append([]Record(nil), input.Records...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].CapturedAt.Equal(records[j].CapturedAt) {
			return records[i].RequestID < records[j].RequestID
		}
		return records[i].CapturedAt.Before(records[j].CapturedAt)
	})
	return publishRecordSource(ctx, store, input, func(yield func(Record) error) error {
		for _, record := range records {
			if err := yield(record); err != nil {
				return err
			}
		}
		return nil
	})
}

type recordSource func(func(Record) error) error

func publishRecordSource(ctx context.Context, store Store, input PublishInput, source recordSource) (Manifest, error) {
	var manifest Manifest
	if store == nil {
		return manifest, errors.New("qa bundle store is required")
	}
	if source == nil {
		return manifest, errors.New("qa bundle record source is required")
	}
	prefix, err := validateObjectPrefix(input.Prefix)
	if err != nil {
		return manifest, err
	}
	input.DataFrom = input.DataFrom.UTC()
	input.DataUntil = input.DataUntil.UTC()
	input.ArchiveWatermark = input.ArchiveWatermark.UTC()
	if !input.DataUntil.Equal(input.DataFrom.Add(24*time.Hour)) || !input.ArchiveWatermark.Equal(input.DataUntil) {
		return manifest, errors.New("qa bundle window must be the 24 hours ending at the archive watermark")
	}
	if input.MaxRecordsPerPage <= 0 {
		input.MaxRecordsPerPage = defaultPageRecordLimit
	}
	if input.MaxCompressedPageBytes <= 0 {
		input.MaxCompressedPageBytes = defaultPageByteLimit
	}
	if input.MaxUncompressedPageBytes <= 0 {
		input.MaxUncompressedPageBytes = defaultPageMemoryLimit
	}

	manifest = Manifest{
		SchemaVersion:    SchemaVersion,
		Generation:       path.Base(prefix),
		DataFrom:         input.DataFrom,
		DataUntil:        input.DataUntil,
		ArchiveWatermark: input.ArchiveWatermark,
		ManifestKey:      prefix + "/manifest.json",
	}
	pageNumber := 1
	var pageRecords []Record
	var pageEncoded []byte
	pageUncompressedBytes := 0
	flushPage := func() error {
		if len(pageRecords) == 0 {
			return nil
		}
		key := fmt.Sprintf("%s/pages/%06d.json.gz", prefix, pageNumber)
		if err := createOrVerify(ctx, store, key, pageEncoded, ObjectMetadata{ContentType: "application/json", ContentEncoding: "gzip"}); err != nil {
			return err
		}
		manifest.Pages = append(manifest.Pages, PageDescriptor{
			Page: pageNumber, Key: key, RecordCount: len(pageRecords),
			CompressedBytes: int64(len(pageEncoded)), SHA256: sha256Hex(pageEncoded),
		})
		pageNumber++
		pageRecords = nil
		pageEncoded = nil
		pageUncompressedBytes = 0
		return nil
	}

	var previousCapturedAt time.Time
	previousRequestID := ""
	havePrevious := false
	err = source(func(record Record) error {
		record.CapturedAt = record.CapturedAt.UTC()
		if strings.TrimSpace(record.RequestID) == "" {
			return errors.New("qa bundle record request id is required")
		}
		if record.CapturedAt.Before(input.DataFrom) || !record.CapturedAt.Before(input.DataUntil) {
			return fmt.Errorf("qa bundle record %s is outside the data window", record.RequestID)
		}
		if havePrevious && (record.CapturedAt.Before(previousCapturedAt) ||
			(record.CapturedAt.Equal(previousCapturedAt) && record.RequestID <= previousRequestID)) {
			return fmt.Errorf("qa bundle projection contains duplicate or unordered identity %d/%s", record.CapturedAt.UnixMicro(), record.RequestID)
		}
		recordBody, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(recordBody) > input.MaxUncompressedPageBytes {
			return fmt.Errorf("qa bundle record %s exceeds uncompressed page limit", record.RequestID)
		}
		if len(pageRecords) > 0 && pageUncompressedBytes+len(recordBody) > input.MaxUncompressedPageBytes {
			if err := flushPage(); err != nil {
				return err
			}
		}
		if len(pageRecords) == input.MaxRecordsPerPage {
			if err := flushPage(); err != nil {
				return err
			}
		}
		candidateRecords := append(pageRecords, record)
		candidate, err := encodePage(Page{SchemaVersion: SchemaVersion, Page: pageNumber, Records: candidateRecords})
		if err != nil {
			return err
		}
		if len(candidate) > input.MaxCompressedPageBytes {
			if len(pageRecords) == 0 {
				return fmt.Errorf("qa bundle record %s exceeds compressed page limit", record.RequestID)
			}
			if err := flushPage(); err != nil {
				return err
			}
			candidateRecords = []Record{record}
			candidate, err = encodePage(Page{SchemaVersion: SchemaVersion, Page: pageNumber, Records: candidateRecords})
			if err != nil {
				return err
			}
			if len(candidate) > input.MaxCompressedPageBytes {
				return fmt.Errorf("qa bundle record %s exceeds compressed page limit", record.RequestID)
			}
		}
		pageRecords = candidateRecords
		pageEncoded = candidate
		pageUncompressedBytes += len(recordBody)
		manifest.RecordCount++
		previousCapturedAt = record.CapturedAt
		previousRequestID = record.RequestID
		havePrevious = true
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	if err := flushPage(); err != nil {
		return Manifest{}, err
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if err := createOrVerify(ctx, store, manifest.ManifestKey, manifestBody, ObjectMetadata{ContentType: "application/json"}); err != nil {
		return Manifest{}, fmt.Errorf("publish qa bundle manifest: %w", err)
	}
	return manifest, nil
}

func BuildExportZip(ctx context.Context, store Store, manifestKey, outputKey string) (ExportReceipt, error) {
	var receipt ExportReceipt
	manifestKey, err := validateObjectKey(manifestKey)
	if err != nil {
		return receipt, err
	}
	outputKey, err = validateObjectKey(outputKey)
	if err != nil {
		return receipt, err
	}
	manifestBody, err := store.Read(ctx, manifestKey)
	if err != nil {
		return receipt, fmt.Errorf("read committed qa bundle manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		return receipt, fmt.Errorf("decode committed qa bundle manifest: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.RecordCount < 0 || manifestKey != path.Dir(manifestKey)+"/manifest.json" {
		return receipt, errors.New("invalid committed qa bundle manifest")
	}
	prefix := path.Dir(manifestKey) + "/pages/"
	tmp, err := os.CreateTemp("", "qa-bundle-export-*.zip")
	if err != nil {
		return receipt, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	zipWriter := zip.NewWriter(tmp)
	jsonl, err := zipWriter.Create("qa-records.jsonl")
	if err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	writtenRecords := 0
	for index, descriptor := range manifest.Pages {
		if descriptor.Page != index+1 || !strings.HasPrefix(descriptor.Key, prefix) || path.Dir(descriptor.Key) != strings.TrimSuffix(prefix, "/") {
			_ = tmp.Close()
			return receipt, errors.New("qa bundle manifest page key is outside the committed generation")
		}
		compressed, err := store.Read(ctx, descriptor.Key)
		if err != nil {
			_ = tmp.Close()
			return receipt, err
		}
		if int64(len(compressed)) != descriptor.CompressedBytes || sha256Hex(compressed) != descriptor.SHA256 {
			_ = tmp.Close()
			return receipt, errors.New("qa bundle page checksum mismatch")
		}
		page, err := decodePage(compressed)
		if err != nil {
			_ = tmp.Close()
			return receipt, err
		}
		if page.Page != descriptor.Page || len(page.Records) != descriptor.RecordCount {
			_ = tmp.Close()
			return receipt, errors.New("qa bundle page count mismatch")
		}
		for _, record := range page.Records {
			encoded, err := json.Marshal(record)
			if err != nil {
				_ = tmp.Close()
				return receipt, err
			}
			if _, err := jsonl.Write(append(encoded, '\n')); err != nil {
				_ = tmp.Close()
				return receipt, err
			}
			writtenRecords++
		}
	}
	if writtenRecords != manifest.RecordCount {
		_ = tmp.Close()
		return receipt, errors.New("qa bundle export aggregate count mismatch")
	}
	if err := zipWriter.Close(); err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	info, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, tmp); err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	zipSHA256 := hex.EncodeToString(hash.Sum(nil))
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	if err := createFileOrVerify(ctx, store, outputKey, tmp, info.Size(), zipSHA256, ObjectMetadata{ContentType: "application/zip"}); err != nil {
		_ = tmp.Close()
		return receipt, err
	}
	if err := tmp.Close(); err != nil {
		return receipt, err
	}
	return ExportReceipt{
		SchemaVersion: ExportReceiptSchema,
		ManifestKey:   manifestKey,
		StorageKey:    outputKey,
		RecordCount:   writtenRecords,
		SHA256:        zipSHA256,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

func createFileOrVerify(ctx context.Context, store Store, key string, file *os.File, size int64, checksum string, meta ObjectMetadata) error {
	err := store.Create(ctx, key, file, size, meta)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrObjectExists) {
		return err
	}
	existing, openErr := store.Open(ctx, key)
	if openErr != nil {
		return openErr
	}
	defer func() { _ = existing.Body.Close() }()
	if existing.Size != size {
		return fmt.Errorf("qa bundle immutable object conflict: %s", key)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, existing.Body)
	if copyErr != nil {
		return copyErr
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != checksum {
		return fmt.Errorf("qa bundle immutable object conflict: %s", key)
	}
	return nil
}

func encodePage(page Page) ([]byte, error) {
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(page); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decodePage(compressed []byte) (Page, error) {
	var page Page
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return page, err
	}
	defer func() { _ = reader.Close() }()
	if err := json.NewDecoder(io.LimitReader(reader, 64<<20)).Decode(&page); err != nil {
		return page, err
	}
	if page.SchemaVersion != SchemaVersion {
		return page, errors.New("qa bundle page schema mismatch")
	}
	return page, nil
}

func createOrVerify(ctx context.Context, store Store, key string, body []byte, meta ObjectMetadata) error {
	err := store.Create(ctx, key, bytes.NewReader(body), int64(len(body)), meta)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrObjectExists) {
		return err
	}
	existing, readErr := store.Read(ctx, key)
	if readErr != nil {
		return readErr
	}
	if !bytes.Equal(existing, body) {
		return fmt.Errorf("qa bundle immutable object conflict: %s", key)
	}
	return nil
}

func validateObjectPrefix(value string) (string, error) {
	value, err := validateObjectKey(value)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(value, "/") {
		return "", errors.New("qa bundle prefix must not end with slash")
	}
	return value, nil
}

func validateObjectKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return "", errors.New("qa bundle object key is unsafe")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("qa bundle object key is unsafe")
		}
	}
	return value, nil
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
