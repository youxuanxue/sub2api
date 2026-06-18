package qa

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/qarecord"
	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const trajectoryExportFilename = "trajectory.jsonl"

// exportPageSize bounds each keyset-paginated DB read so the export never holds
// the whole result set (or a long-running query / table lock) at once.
const exportPageSize = 500

// ExportUserTrajectoryData builds the trajectory zip for one user/key and uploads
// it to the blob store, returning a presigned download URL. It is designed to run
// off the request path (the export worker calls it) and to never put pressure on
// the live gateway — the 2026-06-17 incident was the old in-memory, unbounded,
// non-cancellable build saturating the shared EBS volume that Postgres also uses.
// Safeguards: keyset-paginated reads, one blob loaded at a time, a temp-file zip
// (constant memory), an I/O throttle between blob reads, ctx cancellation honoured
// every record (client disconnect stops the work), and missing/corrupt blobs
// skipped rather than aborting the whole export.
func (s *Service) ExportUserTrajectoryData(ctx context.Context, userID int64, filter ExportFilter) (*ExportResult, error) {
	tmp, err := os.CreateTemp(s.exportTmpDir(), "traj-export-*.zip")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()

	zipWriter := zip.NewWriter(tmp)
	indexWriter, err := zipWriter.Create(trajectoryExportFilename)
	if err != nil {
		return nil, err
	}

	v2 := strings.EqualFold(strings.TrimSpace(filter.Format), "v2")
	var recordCount, exportedRows, skippedBlobs int

	writeLine := func(v any) error {
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := indexWriter.Write(append(encoded, '\n')); err != nil {
			return err
		}
		exportedRows++
		return nil
	}

	// flush projects one conversation's records (already folded by prefix chain)
	// into JSONL line(s) and releases them. Holding only one conversation at a
	// time keeps peak memory bounded regardless of total record count.
	flush := func(group []trajectory.SourceRecord) error {
		if len(group) == 0 {
			return nil
		}
		if v2 {
			sessions, _, err := trajectory.BuildTrajSessionsV2(group)
			if err != nil {
				return err
			}
			for i := range sessions {
				if err := writeLine(sessions[i]); err != nil {
					return err
				}
			}
			return nil
		}
		rows, _, err := trajectory.ProjectRecords(group)
		if err != nil {
			return err
		}
		for i := range rows {
			if err := writeLine(rows[i]); err != nil {
				return err
			}
		}
		return nil
	}

	var group []trajectory.SourceRecord
	throttle := newExportThrottle()

	err = s.streamExportRecords(ctx, userID, filter, func(record *ent.QARecord) error {
		if err := ctx.Err(); err != nil {
			return err // client disconnect / job cancel — stop the work
		}
		throttle.wait(ctx)
		src, ok := s.loadSourceRecord(ctx, record)
		if !ok {
			skippedBlobs++
			return nil
		}
		recordCount++
		// A record continues the current group if it shares a synth_session_id
		// (synthetic pipelines key on that, and their bodies aren't prefix chains)
		// OR its messages extend the previous request as a prefix (real agent
		// conversations). Otherwise it opens a new conversation, so flush.
		if len(group) > 0 {
			prev := group[len(group)-1]
			continues := sameSynthSession(prev.Record, record) ||
				trajectory.RequestMessagesContinue(prev.Blob.Request.Body, src.Blob.Request.Body)
			if !continues {
				if err := flush(group); err != nil {
					return err
				}
				group = group[:0]
			}
		}
		group = append(group, src)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := flush(group); err != nil {
		return nil, err
	}

	if exportedRows == 0 {
		return nil, fmt.Errorf("trajectory export has no rows")
	}
	if skippedBlobs > 0 {
		logger.L().Warn("traj export: some evidence blobs were skipped",
			zap.Int64("user_id", userID), zap.Int("skipped", skippedBlobs), zap.Int("exported_records", recordCount))
	}

	if err := zipWriter.Close(); err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	key := exportStorageKey(userID, filter)
	signedAt := time.Now().UTC()
	if _, err := s.exportStore.PutReader(ctx, key, tmp, "application/zip"); err != nil {
		return nil, err
	}
	url, err := s.exportStore.PresignURL(ctx, key, presignedURLTTL)
	if err != nil {
		return nil, err
	}
	// Preserve the historical RecordCount semantics: v2 reports source records,
	// the legacy v1 projection reports exported rows (one source record explodes
	// into several message-kind rows).
	count := recordCount
	if !v2 {
		count = exportedRows
	}
	return &ExportResult{
		DownloadURL: url,
		ExpiresAt:   signedAt.Add(presignedURLTTL),
		RecordCount: count,
		StorageKey:  key,
	}, nil
}

// exportTmpDir is where the streaming zip is staged before upload. It lives on
// the data volume (next to qa_dlq) so large exports don't risk filling a small
// root/tmpfs; an empty dlqDir (tests) falls back to the OS temp dir.
//
// QA_EXPORT_TMP_DIR overrides the staging location — set it to a volume separate
// from the Postgres data volume to fully isolate the export's write I/O from the
// DB (pairs with moving qa blobs to S3 for read isolation).
func (s *Service) exportTmpDir() string {
	if v := strings.TrimSpace(os.Getenv("QA_EXPORT_TMP_DIR")); v != "" {
		_ = os.MkdirAll(v, 0o755)
		return v
	}
	if strings.TrimSpace(s.dlqDir) == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(s.dlqDir), "qa_exports_tmp")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// sameSynthSession reports whether two records belong to the same synthetic
// pipeline session (a non-empty, equal synth_session_id). Synthetic fixtures
// key on this rather than on a request-message prefix chain.
func sameSynthSession(a, b *ent.QARecord) bool {
	sa, sb := "", ""
	if a != nil && a.SynthSessionID != nil {
		sa = strings.TrimSpace(*a.SynthSessionID)
	}
	if b != nil && b.SynthSessionID != nil {
		sb = strings.TrimSpace(*b.SynthSessionID)
	}
	return sa != "" && sa == sb
}

// loadSourceRecord loads + decodes one record's evidence blob. A nil/empty blob
// uri, an unreadable blob (e.g. pruned by the retention cleanup mid-export), or
// an undecodable payload yields ok=false so the caller skips it — one bad blob
// must never abort the whole export.
func (s *Service) loadSourceRecord(ctx context.Context, record *ent.QARecord) (trajectory.SourceRecord, bool) {
	if record == nil || record.BlobURI == nil {
		return trajectory.SourceRecord{}, false
	}
	blobURI := strings.TrimSpace(*record.BlobURI)
	if blobURI == "" {
		return trajectory.SourceRecord{}, false
	}
	payload, err := s.loadEvidenceBlob(ctx, blobURI)
	if err != nil {
		logger.L().Warn("traj export: skip unreadable evidence blob", zap.String("blob_uri", blobURI), zap.Error(err))
		return trajectory.SourceRecord{}, false
	}
	blob, err := trajectory.DecodeEvidenceBlob(payload)
	if err != nil {
		logger.L().Warn("traj export: skip undecodable evidence blob", zap.String("blob_uri", blobURI), zap.Error(err))
		return trajectory.SourceRecord{}, false
	}
	return trajectory.SourceRecord{Record: record, Blob: blob}, true
}

// streamExportRecords walks the matching qa_records in (created_at, id) order via
// keyset pagination, invoking fn per record. Keyset (not OFFSET) keeps each page a
// cheap index range scan and avoids a single huge query / long transaction.
func (s *Service) streamExportRecords(ctx context.Context, userID int64, filter ExportFilter, fn func(*ent.QARecord) error) error {
	predicates := s.exportPredicates(userID, filter)
	var lastCreated time.Time
	var lastID int64
	first := true
	for {
		q := s.client.QARecord.Query().Where(predicates...)
		if !first {
			q = q.Where(qarecord.Or(
				qarecord.CreatedAtGT(lastCreated),
				qarecord.And(qarecord.CreatedAtEQ(lastCreated), qarecord.IDGT(lastID)),
			))
		}
		batch, err := q.
			Order(ent.Asc(qarecord.FieldCreatedAt), ent.Asc(qarecord.FieldID)).
			Limit(exportPageSize).
			All(ctx)
		if err != nil {
			return err
		}
		for _, rec := range batch {
			if err := fn(rec); err != nil {
				return err
			}
		}
		if len(batch) < exportPageSize {
			return nil
		}
		last := batch[len(batch)-1]
		lastCreated, lastID, first = last.CreatedAt, last.ID, false
	}
}

// exportThrottle paces blob reads so a bulk export can't saturate the EBS volume
// shared with Postgres. Default ~2ms/blob (~500 reads/s); tune via
// QA_EXPORT_BLOB_MIN_INTERVAL_MS (0 disables).
type exportThrottle struct {
	minInterval time.Duration
	last        time.Time
}

func newExportThrottle() *exportThrottle {
	iv := 2 * time.Millisecond
	if v := strings.TrimSpace(os.Getenv("QA_EXPORT_BLOB_MIN_INTERVAL_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			iv = time.Duration(n) * time.Millisecond
		}
	}
	return &exportThrottle{minInterval: iv}
}

func (t *exportThrottle) wait(ctx context.Context) {
	if t == nil || t.minInterval <= 0 {
		return
	}
	if !t.last.IsZero() {
		if elapsed := time.Since(t.last); elapsed < t.minInterval {
			select {
			case <-time.After(t.minInterval - elapsed):
			case <-ctx.Done():
			}
		}
	}
	t.last = time.Now()
}

func (s *Service) DownloadUserTrajectoryExport(ctx context.Context, userID int64, key string) ([]byte, error) {
	key = strings.TrimSpace(key)
	// user_id-first prefix is the ownership boundary: a key that doesn't begin
	// with this user's namespace is refused before any store access.
	prefix := fmt.Sprintf("traj-exports/%d/", userID)
	if key == "" || !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".zip") {
		return nil, fs.ErrPermission
	}
	if strings.Contains(key, "\\") || strings.HasPrefix(key, "/") || hasUnsafePathSegment(key) {
		return nil, fs.ErrPermission
	}
	// TTL gate from the key's own timestamp (manual: trailing nanos; auto: the
	// dated filename) — see exportKeyExpired. This is the localfs proxy path;
	// S3 users download via the presigned URL directly. The host cleanup / S3
	// lifecycle is the real expirer; this is the belt-and-suspenders 404.
	if exportKeyExpired(key, time.Now().UTC()) {
		return nil, fs.ErrNotExist
	}
	body, err := s.exportStore.Get(ctx, key)
	if err != nil && os.IsNotExist(err) {
		return nil, fs.ErrNotExist
	}
	return body, err
}

// exportStorageKey lays out the blob-store key for one export. user_id is always
// the first segment so DownloadUserTrajectoryExport's ownership prefix check
// holds. Auto (daily cron) keys embed the date and are therefore idempotent
// across same-day re-runs; manual keys use unix-nanos for per-click uniqueness.
//
//	traj-exports/<user>/<key|all>/auto/<YYYY-MM-DD>.zip
//	traj-exports/<user>/<key|all>/manual/<unix_nanos>.zip
func exportStorageKey(userID int64, f ExportFilter) string {
	keySeg := "all"
	if f.APIKeyID != nil {
		keySeg = strconv.FormatInt(*f.APIKeyID, 10)
	}
	base := fmt.Sprintf("traj-exports/%d/%s", userID, keySeg)
	if strings.EqualFold(strings.TrimSpace(f.Kind), exportKindAuto) {
		day := f.Since.UTC().Format("2006-01-02")
		return fmt.Sprintf("%s/auto/%s.zip", base, day)
	}
	return fmt.Sprintf("%s/manual/%d.zip", base, time.Now().UnixNano())
}

// UserTrajExportEnabled reports whether the admin has granted this user the
// traj_export_enabled switch. It is the handler-layer authorization backstop
// behind the UI visibility gate — a hand-crafted POST cannot bypass it. A
// missing user resolves to false (denied) rather than an error.
func (s *Service) UserTrajExportEnabled(ctx context.Context, userID int64) (bool, error) {
	u, err := s.client.User.Get(ctx, userID)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return u.TrajExportEnabled, nil
}

// exportPredicates builds the qa_records WHERE clause shared by the streaming
// export and queryExportRecords. user_id scope is always present; APIKeyID and
// Platform AND-narrow it; SynthSessionID overrides the Since/Until window.
func (s *Service) exportPredicates(userID int64, filter ExportFilter) []predicate.QARecord {
	predicates := []predicate.QARecord{qarecord.UserIDEQ(userID)}
	if filter.APIKeyID != nil {
		predicates = append(predicates, qarecord.APIKeyIDEQ(*filter.APIKeyID))
	}
	if p := strings.TrimSpace(filter.Platform); p != "" {
		predicates = append(predicates, qarecord.PlatformEQ(p))
	}
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
	return predicates
}

func (s *Service) queryExportRecords(ctx context.Context, userID int64, filter ExportFilter) ([]*ent.QARecord, error) {
	return s.client.QARecord.Query().
		Where(s.exportPredicates(userID, filter)...).
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
