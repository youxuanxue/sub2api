package archive

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// RecordRow is the parquet projection of qa_records metadata (design §8.2).
type RecordRow struct {
	RequestID       string  `parquet:"request_id"`
	TrajectoryID    *string `parquet:"trajectory_id,optional"`
	UserID          int64   `parquet:"user_id"`
	GroupID         *int64  `parquet:"group_id,optional"`
	APIKeyID        int64   `parquet:"api_key_id"`
	AccountID       *int64  `parquet:"account_id,optional"`
	Platform        string  `parquet:"platform"`
	Provider        *string `parquet:"provider,optional"`
	RequestedModel  string  `parquet:"requested_model"`
	UpstreamModel   *string `parquet:"upstream_model,optional"`
	StatusCode      int     `parquet:"status_code"`
	Success         bool    `parquet:"success"`
	DurationMS      int64   `parquet:"duration_ms"`
	Stream          bool    `parquet:"stream"`
	InputTokens     int     `parquet:"input_tokens"`
	OutputTokens    int     `parquet:"output_tokens"`
	RequestSHA256   string  `parquet:"request_sha256"`
	ResponseSHA256  string  `parquet:"response_sha256"`
	BlobURI         *string `parquet:"blob_uri,optional"`
	RequestBlobURI  *string `parquet:"request_blob_uri,optional"`
	ResponseBlobURI *string `parquet:"response_blob_uri,optional"`
	StreamBlobURI   *string `parquet:"stream_blob_uri,optional"`
	CaptureStatus   string  `parquet:"capture_status"`
	CreatedAt       int64   `parquet:"created_at, timestamp(microsecond)"`
}

type evidenceIndexLine struct {
	RequestID string `json:"request_id"`
	BlobField string `json:"blob_field"`
	BlobURI   string `json:"blob_uri"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
	SHA256    string `json:"sha256"`
}

type rowStats struct {
	recordCount  int64
	blobRefCount int64
	logicalBytes int64
}

func localEvidencePath(blobRoot, blobURI string) (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(blobRoot))
	if err != nil || root == "" {
		return "", fmt.Errorf("resolve blob root: %w", err)
	}
	blobURI = strings.TrimSpace(blobURI)
	var candidate string
	switch {
	case strings.HasPrefix(blobURI, "file://"):
		candidate = strings.TrimPrefix(blobURI, "file://")
		if !filepath.IsAbs(candidate) {
			return "", fmt.Errorf("evidence path outside blob root")
		}
	case strings.HasPrefix(blobURI, "mem://"):
		candidate = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(blobURI, "mem://")))
	default:
		if blobURI == "" {
			return "", fmt.Errorf("empty evidence URI")
		}
		candidate = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(blobURI, "/")))
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil || !pathWithinRoot(root, candidate) {
		return "", fmt.Errorf("evidence path outside blob root")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve blob root: %w", err)
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(realRoot, realCandidate) {
		return "", fmt.Errorf("evidence path outside blob root")
	}
	return realCandidate, nil
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
