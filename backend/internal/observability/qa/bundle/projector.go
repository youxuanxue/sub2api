package bundle

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

const maxProjectedEvidenceBytes = 8 << 20

var projectedRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type evidenceProjection struct {
	file string
	key  string
}

var evidenceProjections = [...]evidenceProjection{
	{file: "blob_uri.bin", key: "evidence"},
	{file: "request_blob_uri.bin", key: "request"},
	{file: "response_blob_uri.bin", key: "response"},
	{file: "stream_blob_uri.bin", key: "stream"},
}

// PublishVerifiedCommits projects restored, checksum-verified raw commits
// directly into bounded immutable pages for one authorized scope.
func PublishVerifiedCommits(
	ctx context.Context,
	store Store,
	input PublishInput,
	commits []archive.VerifiedCommit,
	userID, apiKeyID int64,
) (Manifest, error) {
	if userID <= 0 || apiKeyID <= 0 {
		return Manifest{}, errors.New("qa bundle projection scope is invalid")
	}
	return publishRecordSource(ctx, store, input, func(yield func(Record) error) error {
		for _, commit := range commits {
			if err := visitVerifiedSegments(commit.Segments, userID, apiKeyID, yield); err != nil {
				return err
			}
		}
		return nil
	})
}

func visitVerifiedSegments(segments []archive.VerifiedSegment, userID, apiKeyID int64, yield func(Record) error) error {
	if userID <= 0 || apiKeyID <= 0 || yield == nil {
		return errors.New("qa bundle projection scope is invalid")
	}
	streams := make([]*projectedSegmentStream, 0, len(segments))
	defer func() {
		for _, stream := range streams {
			_ = stream.close()
		}
	}()
	queue := projectedSegmentHeap{}
	for _, segment := range segments {
		if segment.RestoreDir == "" {
			return errors.New("qa bundle projection requires a restored verified segment")
		}
		file, err := os.Open(filepath.Join(segment.RestoreDir, "records.parquet"))
		if err != nil {
			return err
		}
		stream := &projectedSegmentStream{
			index: len(streams), restoreDir: segment.RestoreDir, file: file,
			reader: parquet.NewGenericReader[archive.RecordRow](file), userID: userID, apiKeyID: apiKeyID,
		}
		streams = append(streams, stream)
		ok, err := stream.advance()
		if err != nil {
			return err
		}
		if ok {
			heap.Push(&queue, stream)
		}
	}
	heap.Init(&queue)
	var previousCreatedAt int64
	previousRequestID := ""
	havePrevious := false
	for queue.Len() > 0 {
		stream := heap.Pop(&queue).(*projectedSegmentStream)
		row := stream.current
		if havePrevious && row.CreatedAt == previousCreatedAt && row.RequestID == previousRequestID {
			return fmt.Errorf("qa bundle projection contains duplicate identity %d/%s", row.CreatedAt, row.RequestID)
		}
		record, err := projectRecord(stream.restoreDir, row)
		if err != nil {
			return err
		}
		if err := yield(record); err != nil {
			return err
		}
		previousCreatedAt, previousRequestID, havePrevious = row.CreatedAt, row.RequestID, true
		ok, err := stream.advance()
		if err != nil {
			return err
		}
		if ok {
			heap.Push(&queue, stream)
		}
	}
	return nil
}

type projectedSegmentStream struct {
	index      int
	restoreDir string
	file       *os.File
	reader     *parquet.GenericReader[archive.RecordRow]
	userID     int64
	apiKeyID   int64
	current    archive.RecordRow
	closed     bool
	exhausted  bool
}

func (s *projectedSegmentStream) advance() (bool, error) {
	if s.closed {
		return false, nil
	}
	if s.exhausted {
		return false, s.close()
	}
	row := make([]archive.RecordRow, 1)
	for {
		n, err := s.reader.Read(row)
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if n == 1 {
			s.exhausted = errors.Is(err, io.EOF)
			if row[0].UserID == s.userID && row[0].APIKeyID == s.apiKeyID {
				s.current = row[0]
				return true, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return false, s.close()
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, io.ErrNoProgress
		}
	}
}

func (s *projectedSegmentStream) close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	readerErr := s.reader.Close()
	fileErr := s.file.Close()
	if readerErr != nil {
		return readerErr
	}
	return fileErr
}

type projectedSegmentHeap []*projectedSegmentStream

func (h projectedSegmentHeap) Len() int { return len(h) }
func (h projectedSegmentHeap) Less(i, j int) bool {
	left, right := h[i].current, h[j].current
	if left.CreatedAt != right.CreatedAt {
		return left.CreatedAt < right.CreatedAt
	}
	if left.RequestID != right.RequestID {
		return left.RequestID < right.RequestID
	}
	return h[i].index < h[j].index
}
func (h projectedSegmentHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *projectedSegmentHeap) Push(value any) { *h = append(*h, value.(*projectedSegmentStream)) }
func (h *projectedSegmentHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func projectRecord(restoreDir string, row archive.RecordRow) (Record, error) {
	if !projectedRequestIDPattern.MatchString(row.RequestID) {
		return Record{}, errors.New("qa bundle projection request id is unsafe")
	}
	record := Record{
		RequestID: row.RequestID, TrajectoryID: row.TrajectoryID,
		UserID: row.UserID, GroupID: row.GroupID, APIKeyID: row.APIKeyID,
		ChannelType: row.ChannelType, Platform: row.Platform, Provider: row.Provider,
		RequestedModel: row.RequestedModel, UpstreamModel: row.UpstreamModel,
		StatusCode: row.StatusCode, Success: row.Success, DurationMS: row.DurationMS,
		Stream: row.Stream, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		CaptureStatus: row.CaptureStatus, SynthSessionID: row.SynthSessionID,
		SynthRole: row.SynthRole, SynthEngineerLevel: row.SynthEngineerLevel,
		CapturedAt: time.UnixMicro(row.CreatedAt).UTC(),
	}
	if row.InboundEndpoint != nil {
		record.InboundEndpoint = *row.InboundEndpoint
	}
	record.UpstreamEndpoint = row.UpstreamEndpoint
	record.FirstTokenMS = row.FirstTokenMS
	if row.ToolCallsPresent != nil {
		record.ToolCallsPresent = *row.ToolCallsPresent
	}
	if row.MultimodalPresent != nil {
		record.MultimodalPresent = *row.MultimodalPresent
	}
	if row.CachedTokens != nil {
		record.CachedTokens = int(*row.CachedTokens)
	}
	if row.RedactionVersion != nil {
		record.RedactionVersion = *row.RedactionVersion
	}
	if row.TagsJSON != nil && *row.TagsJSON != "" {
		if err := json.Unmarshal([]byte(*row.TagsJSON), &record.Tags); err != nil {
			return Record{}, fmt.Errorf("decode qa bundle tags for %s: %w", row.RequestID, err)
		}
	}
	if row.DialogSynth != nil {
		record.DialogSynth = *row.DialogSynth
	}
	detail, err := projectEvidence(filepath.Join(restoreDir, "evidence", row.RequestID))
	if err != nil {
		return Record{}, fmt.Errorf("project qa bundle evidence for %s: %w", row.RequestID, err)
	}
	record.Detail = detail
	return record, nil
}

func projectEvidence(dir string) (map[string]json.RawMessage, error) {
	detail := make(map[string]json.RawMessage)
	for _, projection := range evidenceProjections {
		body, err := os.ReadFile(filepath.Join(dir, projection.file))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		decoder, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		decoded, readErr := io.ReadAll(io.LimitReader(decoder, maxProjectedEvidenceBytes+1))
		decoder.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(decoded) > maxProjectedEvidenceBytes || !json.Valid(decoded) {
			return nil, errors.New("evidence is oversized or not valid JSON")
		}
		detail[projection.key] = append(json.RawMessage(nil), decoded...)
	}
	if len(detail) == 0 {
		return nil, nil
	}
	return detail, nil
}
