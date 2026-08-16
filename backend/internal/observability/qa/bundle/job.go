package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

const JobSchemaVersion = "qa-bundle-job-v1"

type JobKind string

const (
	JobKindBundle    JobKind = "bundle"
	JobKindBundleZip JobKind = "bundle_zip"
)

type JobSpec struct {
	SchemaVersion    string    `json:"schema_version"`
	Kind             JobKind   `json:"kind"`
	JobID            string    `json:"job_id"`
	BundleJobID      string    `json:"bundle_job_id,omitempty"`
	UserID           int64     `json:"user_id"`
	APIKeyID         int64     `json:"api_key_id"`
	DataFrom         time.Time `json:"data_from"`
	DataUntil        time.Time `json:"data_until"`
	ArchiveWatermark time.Time `json:"archive_watermark"`
	CommitKeys       []string  `json:"commit_keys,omitempty"`
	GenerationPrefix string    `json:"generation_prefix,omitempty"`
	ManifestKey      string    `json:"manifest_key"`
	OutputKey        string    `json:"output_key,omitempty"`
	SpecKey          string    `json:"-"`
	ReceiptKey       string    `json:"-"`
	FailureKey       string    `json:"-"`
}

type JobReceipt struct {
	SchemaVersion    string    `json:"schema_version"`
	Kind             JobKind   `json:"kind"`
	JobID            string    `json:"job_id"`
	ManifestKey      string    `json:"manifest_key"`
	StorageKey       string    `json:"storage_key,omitempty"`
	DataFrom         time.Time `json:"data_from"`
	DataUntil        time.Time `json:"data_until"`
	ArchiveWatermark time.Time `json:"archive_watermark"`
	RecordCount      int       `json:"record_count"`
	SHA256           string    `json:"sha256,omitempty"`
	CompletedAt      time.Time `json:"completed_at"`
}

type ExecuteDeps struct {
	RestoreRoot  string
	VerifyCommit func(context.Context, archive.ReadOnlyObjectStore, string, string) (archive.VerifiedCommit, error)
	Now          func() time.Time
}

func NewBundleJobSpec(userID, apiKeyID int64, watermark time.Time) JobSpec {
	watermark = watermark.UTC()
	dataFrom := watermark.Add(-24 * time.Hour)
	jobID := deterministicJobID(JobKindBundle, userID, apiKeyID, watermark, "")
	base := jobBase(jobID)
	commitKeys := make([]string, 24)
	for index := range commitKeys {
		hour := dataFrom.Add(time.Duration(index) * time.Hour)
		commitKeys[index] = archive.ShardRelativePrefix(hour) + "/commit.json"
	}
	return JobSpec{
		SchemaVersion: JobSchemaVersion, Kind: JobKindBundle, JobID: jobID,
		UserID: userID, APIKeyID: apiKeyID, DataFrom: dataFrom, DataUntil: watermark,
		ArchiveWatermark: watermark, CommitKeys: commitKeys,
		GenerationPrefix: base + "/generation", ManifestKey: base + "/generation/manifest.json",
		SpecKey: base + "/spec.json", ReceiptKey: base + "/receipt.json", FailureKey: base + "/failure.json",
	}
}

func NewZipJobSpec(bundleSpec JobSpec, manifestKey string) JobSpec {
	jobID := deterministicJobID(JobKindBundleZip, bundleSpec.UserID, bundleSpec.APIKeyID, bundleSpec.ArchiveWatermark, bundleSpec.JobID)
	base := jobBase(jobID)
	return JobSpec{
		SchemaVersion: JobSchemaVersion, Kind: JobKindBundleZip, JobID: jobID,
		BundleJobID: bundleSpec.JobID,
		UserID:      bundleSpec.UserID, APIKeyID: bundleSpec.APIKeyID,
		DataFrom: bundleSpec.DataFrom, DataUntil: bundleSpec.DataUntil, ArchiveWatermark: bundleSpec.ArchiveWatermark,
		ManifestKey: manifestKey, OutputKey: base + "/export.zip",
		SpecKey: base + "/spec.json", ReceiptKey: base + "/receipt.json", FailureKey: base + "/failure.json",
	}
}

func ParseJobSpec(body []byte) (JobSpec, error) {
	var spec JobSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		return JobSpec{}, fmt.Errorf("decode qa bundle job spec: %w", err)
	}
	spec.setDerivedControlKeys()
	if err := spec.Validate(); err != nil {
		return JobSpec{}, err
	}
	return spec, nil
}

func PublishJobSpec(ctx context.Context, store Store, spec JobSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	return createOrVerify(ctx, store, spec.SpecKey, body, ObjectMetadata{ContentType: "application/json"})
}

// SpecKeyForJobID resolves the only durable registry key for a Bundle job.
func SpecKeyForJobID(jobID string) (string, error) {
	jobID = strings.TrimSpace(jobID)
	if len(jobID) != 64 {
		return "", errors.New("qa bundle job id is invalid")
	}
	for _, char := range jobID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", errors.New("qa bundle job id is invalid")
		}
	}
	return jobBase(jobID) + "/spec.json", nil
}

// ReadJobSpec reads and validates the immutable S3 job registry entry.
func ReadJobSpec(ctx context.Context, store Store, jobID string) (JobSpec, bool, error) {
	if store == nil {
		return JobSpec{}, false, errors.New("qa bundle store is required")
	}
	key, err := SpecKeyForJobID(jobID)
	if err != nil {
		return JobSpec{}, false, err
	}
	found, err := store.Head(ctx, key)
	if err != nil || !found {
		return JobSpec{}, false, err
	}
	body, err := store.Read(ctx, key)
	if err != nil {
		return JobSpec{}, false, err
	}
	spec, err := ParseJobSpec(body)
	if err != nil {
		return JobSpec{}, false, err
	}
	if spec.JobID != strings.TrimSpace(jobID) || spec.SpecKey != key {
		return JobSpec{}, false, errors.New("qa bundle spec identity does not match registry key")
	}
	return spec, true, nil
}

func (s JobSpec) Validate() error {
	if s.SchemaVersion != JobSchemaVersion || s.UserID <= 0 || s.APIKeyID <= 0 {
		return errors.New("qa bundle job identity is invalid")
	}
	if s.DataFrom.Location() != time.UTC || s.DataUntil.Location() != time.UTC || s.ArchiveWatermark.Location() != time.UTC ||
		!s.DataFrom.Equal(s.DataFrom.Truncate(time.Hour)) || !s.DataUntil.Equal(s.DataFrom.Add(24*time.Hour)) || !s.ArchiveWatermark.Equal(s.DataUntil) {
		return errors.New("qa bundle job window is invalid")
	}
	base := jobBase(s.JobID)
	if s.SpecKey != base+"/spec.json" || s.ReceiptKey != base+"/receipt.json" || s.FailureKey != base+"/failure.json" {
		return errors.New("qa bundle job control keys are invalid")
	}
	switch s.Kind {
	case JobKindBundle:
		if s.JobID != deterministicJobID(s.Kind, s.UserID, s.APIKeyID, s.ArchiveWatermark, "") ||
			s.BundleJobID != "" || s.GenerationPrefix != base+"/generation" || s.ManifestKey != s.GenerationPrefix+"/manifest.json" || s.OutputKey != "" || len(s.CommitKeys) != 24 {
			return errors.New("qa bundle build job is invalid")
		}
		for index, key := range s.CommitKeys {
			expected := archive.ShardRelativePrefix(s.DataFrom.Add(time.Duration(index)*time.Hour)) + "/commit.json"
			if key != expected {
				return fmt.Errorf("qa bundle commit key %d is invalid", index)
			}
		}
	case JobKindBundleZip:
		expectedBundleBase := jobBase(s.BundleJobID)
		if s.JobID != deterministicJobID(s.Kind, s.UserID, s.APIKeyID, s.ArchiveWatermark, s.BundleJobID) ||
			len(s.CommitKeys) != 0 || s.GenerationPrefix != "" || s.OutputKey != base+"/export.zip" ||
			s.ManifestKey != expectedBundleBase+"/generation/manifest.json" {
			return errors.New("qa bundle zip job is invalid")
		}
		manifestKey, err := validateObjectKey(s.ManifestKey)
		if err != nil || manifestKey != s.ManifestKey || !strings.HasSuffix(manifestKey, "/generation/manifest.json") {
			return errors.New("qa bundle zip manifest key is invalid")
		}
	default:
		return errors.New("qa bundle job kind is invalid")
	}
	return nil
}

func (s *JobSpec) setDerivedControlKeys() {
	base := jobBase(s.JobID)
	s.SpecKey = base + "/spec.json"
	s.ReceiptKey = base + "/receipt.json"
	s.FailureKey = base + "/failure.json"
}

func ExecuteJob(ctx context.Context, spec JobSpec, rawStore archive.ReadOnlyObjectStore, outputStore Store, deps ExecuteDeps) (JobReceipt, error) {
	var receipt JobReceipt
	if err := spec.Validate(); err != nil {
		return receipt, err
	}
	if outputStore == nil {
		return receipt, errors.New("qa bundle output store is required")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	receipt = JobReceipt{
		SchemaVersion: JobSchemaVersion, Kind: spec.Kind, JobID: spec.JobID,
		ManifestKey: spec.ManifestKey, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
		ArchiveWatermark: spec.ArchiveWatermark,
	}
	switch spec.Kind {
	case JobKindBundle:
		if rawStore == nil {
			return JobReceipt{}, errors.New("qa raw archive store is required")
		}
		verify := deps.VerifyCommit
		if verify == nil {
			verify = archive.VerifyCommit
		}
		root := strings.TrimSpace(deps.RestoreRoot)
		if root == "" {
			root = os.TempDir()
		}
		workDir, err := os.MkdirTemp(root, "qa-bundle-worker-*")
		if err != nil {
			return JobReceipt{}, err
		}
		defer func() { _ = os.RemoveAll(workDir) }()
		var verified []archive.VerifiedCommit
		for index, commitKey := range spec.CommitKeys {
			commit, err := verify(ctx, rawStore, commitKey, filepath.Join(workDir, fmt.Sprintf("%02d", index)))
			if err != nil {
				closeVerifiedCommits(verified)
				return JobReceipt{}, fmt.Errorf("verify qa raw commit %d: %w", index, err)
			}
			expected := spec.DataFrom.Add(time.Duration(index) * time.Hour)
			if !commit.Document.WindowStart.Equal(expected) || !commit.Document.WindowEnd.Equal(expected.Add(time.Hour)) {
				_ = commit.Close()
				closeVerifiedCommits(verified)
				return JobReceipt{}, errors.New("qa raw commit window does not match job spec")
			}
			verified = append(verified, commit)
		}
		defer closeVerifiedCommits(verified)
		var segments []archive.VerifiedSegment
		for index := range verified {
			segments = append(segments, verified[index].Segments...)
		}
		records, err := ProjectVerifiedSegments(segments, spec.UserID, spec.APIKeyID)
		if err != nil {
			return JobReceipt{}, err
		}
		manifest, err := Publish(ctx, outputStore, PublishInput{
			Prefix: spec.GenerationPrefix, DataFrom: spec.DataFrom, DataUntil: spec.DataUntil,
			ArchiveWatermark: spec.ArchiveWatermark, Records: records,
		})
		if err != nil {
			return JobReceipt{}, err
		}
		receipt.RecordCount = manifest.RecordCount
	case JobKindBundleZip:
		exportReceipt, err := BuildExportZip(ctx, outputStore, spec.ManifestKey, spec.OutputKey)
		if err != nil {
			return JobReceipt{}, err
		}
		receipt.StorageKey = exportReceipt.StorageKey
		receipt.RecordCount = exportReceipt.RecordCount
		receipt.SHA256 = exportReceipt.SHA256
	}
	receipt.CompletedAt = now().UTC()
	body, err := json.Marshal(receipt)
	if err != nil {
		return JobReceipt{}, err
	}
	if err := createOrVerify(ctx, outputStore, spec.ReceiptKey, body, ObjectMetadata{ContentType: "application/json"}); err != nil {
		return JobReceipt{}, fmt.Errorf("publish qa bundle job receipt: %w", err)
	}
	return receipt, nil
}

func deterministicJobID(kind JobKind, userID, apiKeyID int64, watermark time.Time, parent string) string {
	value := fmt.Sprintf("%s:%d:%d:%s:%s:%s", JobSchemaVersion, userID, apiKeyID, watermark.UTC().Format(time.RFC3339), kind, parent)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func jobBase(jobID string) string {
	return "qa-bundles/v1/jobs/" + jobID
}

func closeVerifiedCommits(commits []archive.VerifiedCommit) {
	for index := range commits {
		_ = commits[index].Close()
	}
}
