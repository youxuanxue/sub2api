package qa

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/qarecord"
	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
)

const trajectoryExportFilename = "trajectory.jsonl"

func (s *Service) ExportUserTrajectoryData(ctx context.Context, userID int64, filter ExportFilter) (*ExportResult, error) {
	records, err := s.queryExportRecords(ctx, userID, filter)
	if err != nil {
		return nil, err
	}

	sources := make([]trajectory.SourceRecord, 0, len(records))
	for _, record := range records {
		if record == nil || record.BlobURI == nil {
			continue
		}
		blobURI := strings.TrimSpace(*record.BlobURI)
		if blobURI == "" {
			continue
		}
		payload, err := s.loadEvidenceBlob(ctx, blobURI)
		if err != nil {
			return nil, err
		}
		blob, err := trajectory.DecodeEvidenceBlob(payload)
		if err != nil {
			return nil, err
		}
		sources = append(sources, trajectory.SourceRecord{Record: record, Blob: blob})
	}

	rows, summary, err := trajectory.ProjectRecords(sources)
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("trajectory export has no rows")
	}

	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)
	indexWriter, err := zipWriter.Create(trajectoryExportFilename)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		if _, err := indexWriter.Write(append(encoded, '\n')); err != nil {
			return nil, err
		}
	}
	if err := zipWriter.Close(); err != nil {
		return nil, err
	}

	key := fmt.Sprintf("traj-exports/%d/%d.zip", userID, time.Now().UnixNano())
	signedAt := time.Now().UTC()
	if _, err := s.store.Put(ctx, key, buf.Bytes(), "application/zip"); err != nil {
		return nil, err
	}
	url, err := s.store.PresignURL(ctx, key, presignedURLTTL)
	if err != nil {
		return nil, err
	}
	return &ExportResult{
		DownloadURL: url,
		ExpiresAt:   signedAt.Add(presignedURLTTL),
		RecordCount: summary.RecordCount,
		StorageKey:  key,
	}, nil
}

func (s *Service) DownloadUserTrajectoryExport(ctx context.Context, userID int64, key string) ([]byte, error) {
	key = strings.TrimSpace(key)
	prefix := fmt.Sprintf("traj-exports/%d/", userID)
	if key == "" || !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".zip") {
		return nil, fs.ErrPermission
	}
	if strings.Contains(key, "\\") || strings.HasPrefix(key, "/") || hasUnsafePathSegment(key) {
		return nil, fs.ErrPermission
	}
	if exportKeyExpired(key, time.Now().UTC()) {
		return nil, fs.ErrNotExist
	}
	body, err := s.store.Get(ctx, key)
	if err != nil && os.IsNotExist(err) {
		return nil, fs.ErrNotExist
	}
	return body, err
}

func (s *Service) queryExportRecords(ctx context.Context, userID int64, filter ExportFilter) ([]*ent.QARecord, error) {
	predicates := []predicate.QARecord{qarecord.UserIDEQ(userID)}
	if synthSession := strings.TrimSpace(filter.SynthSessionID); synthSession != "" {
		predicates = append(predicates, qarecord.SynthSessionIDEQ(synthSession))
	} else {
		if !filter.Since.IsZero() {
			predicates = append(predicates, qarecord.CreatedAtGTE(filter.Since))
		}
		if !filter.Until.IsZero() {
			predicates = append(predicates, qarecord.CreatedAtLTE(filter.Until))
		}
	}
	if role := strings.TrimSpace(filter.SynthRole); role != "" {
		predicates = append(predicates, qarecord.SynthRoleEQ(role))
	}
	return s.client.QARecord.Query().
		Where(predicates...).
		Order(ent.Asc(qarecord.FieldCreatedAt)).
		All(ctx)
}

func (s *Service) loadEvidenceBlob(ctx context.Context, blobURI string) ([]byte, error) {
	blobURI = strings.TrimSpace(blobURI)
	switch {
	case strings.HasPrefix(blobURI, "file://"):
		return os.ReadFile(strings.TrimPrefix(blobURI, "file://"))
	case strings.HasPrefix(blobURI, "mem://"):
		return s.store.Get(ctx, strings.TrimPrefix(blobURI, "mem://"))
	default:
		key := s.keyFromBlobURI(blobURI)
		if key == "" {
			return nil, fmt.Errorf("unsupported blob uri: %s", blobURI)
		}
		return s.store.Get(ctx, key)
	}
}
