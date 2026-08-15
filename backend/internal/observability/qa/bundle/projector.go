package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// ProjectVerifiedSegments reads only locally restored, checksum-verified raw
// segments and projects records for one authorized user/API-key scope.
func ProjectVerifiedSegments(segments []archive.VerifiedSegment, userID, apiKeyID int64) ([]Record, error) {
	if userID <= 0 || apiKeyID <= 0 {
		return nil, errors.New("qa bundle projection scope is invalid")
	}
	var out []Record
	seen := make(map[string]struct{})
	for _, segment := range segments {
		if segment.RestoreDir == "" {
			return nil, errors.New("qa bundle projection requires a restored verified segment")
		}
		file, err := os.Open(filepath.Join(segment.RestoreDir, "records.parquet"))
		if err != nil {
			return nil, err
		}
		reader := parquet.NewGenericReader[archive.RecordRow](file)
		page := make([]archive.RecordRow, 250)
		for {
			n, readErr := reader.Read(page)
			for _, row := range page[:n] {
				if row.UserID != userID || row.APIKeyID != apiKeyID {
					continue
				}
				identity := fmt.Sprintf("%d/%s", row.CreatedAt, row.RequestID)
				if _, duplicate := seen[identity]; duplicate {
					_ = reader.Close()
					_ = file.Close()
					return nil, fmt.Errorf("qa bundle projection contains duplicate identity %s", identity)
				}
				seen[identity] = struct{}{}
				record, err := projectRecord(segment.RestoreDir, row)
				if err != nil {
					_ = reader.Close()
					_ = file.Close()
					return nil, err
				}
				out = append(out, record)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = reader.Close()
				_ = file.Close()
				return nil, readErr
			}
		}
		if err := reader.Close(); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CapturedAt.Equal(out[j].CapturedAt) {
			return out[i].RequestID < out[j].RequestID
		}
		return out[i].CapturedAt.Before(out[j].CapturedAt)
	})
	return out, nil
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
