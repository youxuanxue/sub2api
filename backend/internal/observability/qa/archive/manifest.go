package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	SegmentKindBase  = "base"
	ManifestSchemaV1 = "qa-archive-segment-v1"
	CommitSchemaV1   = "qa-archive-commit-v1"
)

type SegmentManifest struct {
	SchemaVersion       string    `json:"schema_version"`
	SegmentID           string    `json:"segment_id"`
	SegmentKind         string    `json:"segment_kind"`
	WindowStart         time.Time `json:"window_start"`
	WindowEnd           time.Time `json:"window_end"`
	RecordCount         int64     `json:"record_count"`
	BlobRefCount        int64     `json:"blob_ref_count"`
	BlobPresentCount    int64     `json:"blob_present_count"`
	BlobMissingCount    int64     `json:"blob_missing_count"`
	LogicalBytes        int64     `json:"logical_bytes"`
	ArtifactBytes       int64     `json:"artifact_bytes"`
	RecordsSHA256       string    `json:"records_sha256"`
	EvidencePackSHA256  string    `json:"evidence_pack_sha256,omitempty"`
	EvidenceIndexSHA256 string    `json:"evidence_index_sha256,omitempty"`
}

type CommitDocument struct {
	SchemaVersion   string          `json:"schema_version"`
	WindowStart     time.Time       `json:"window_start"`
	WindowEnd       time.Time       `json:"window_end"`
	Generation      int             `json:"generation"`
	Segments        []CommitSegment `json:"segments"`
	AggregateSHA256 string          `json:"aggregate_sha256"`
	CommittedAt     time.Time       `json:"committed_at"`
}

type CommitSegment struct {
	SegmentID      string `json:"segment_id"`
	SegmentKind    string `json:"segment_kind"`
	ManifestKey    string `json:"manifest_key"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func MarshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
