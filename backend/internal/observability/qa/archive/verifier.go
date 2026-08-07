package archive

import (
	"bufio"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

const (
	IntegrityCorruptArtifact = "corrupt_artifact"
	maxManifestBytes         = 1 << 20
	verifyRowPageSize        = 500
)

type SegmentDescriptor struct {
	Prefix         string
	ManifestKey    string
	ManifestSHA256 string
}

type VerifiedSegment struct {
	Manifest      SegmentManifest
	IdentityPath  string
	IdentityCount int64
	RestoreDir    string
	scratchDir    string
}

type VerifiedCommit struct {
	Document         CommitDocument
	ETag             string
	Segments         []VerifiedSegment
	RecordCount      int64
	BlobRefCount     int64
	BlobPresentCount int64
	BlobMissingCount int64
}

func (v *VerifiedCommit) Close() error {
	if v == nil {
		return nil
	}
	var first error
	for i := range v.Segments {
		if err := v.Segments[i].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (v *VerifiedSegment) Close() error {
	if v == nil || v.scratchDir == "" {
		return nil
	}
	return os.RemoveAll(v.scratchDir)
}

func VerifyCommit(ctx context.Context, store ObjectStore, commitKey, restoreDir string) (_ VerifiedCommit, resultErr error) {
	opened, err := store.Open(ctx, commitKey)
	if err != nil {
		return VerifiedCommit{}, corruptArtifact("commit.json", err)
	}
	if opened.Info.Size > maxManifestBytes {
		_ = opened.Body.Close()
		return VerifiedCommit{}, corruptArtifact("commit.json", fmt.Errorf("object exceeds %d byte limit", maxManifestBytes))
	}
	commitBytes, err := io.ReadAll(io.LimitReader(opened.Body, maxManifestBytes+1))
	closeErr := opened.Body.Close()
	if err != nil {
		return VerifiedCommit{}, corruptArtifact("commit.json", err)
	}
	if closeErr != nil {
		return VerifiedCommit{}, corruptArtifact("commit.json", closeErr)
	}
	if int64(len(commitBytes)) != opened.Info.Size {
		return VerifiedCommit{}, corruptArtifact("commit.json", fmt.Errorf("object size mismatch"))
	}
	var commit CommitDocument
	if err := json.Unmarshal(commitBytes, &commit); err != nil {
		return VerifiedCommit{}, corruptArtifact("commit.json", err)
	}
	if err := validateCommitDocument(commit); err != nil {
		return VerifiedCommit{}, err
	}
	if err := validateCommitLocation(commitKey, commit); err != nil {
		return VerifiedCommit{}, err
	}

	restoreCreated := false
	if restoreDir != "" {
		if err := os.Mkdir(restoreDir, 0o700); err != nil {
			return VerifiedCommit{}, fmt.Errorf("create restore directory: %w", err)
		}
		restoreCreated = true
		defer func() {
			if resultErr != nil && restoreCreated {
				_ = os.RemoveAll(restoreDir)
			}
		}()
		if err := os.Chmod(restoreDir, 0o700); err != nil {
			return VerifiedCommit{}, fmt.Errorf("secure restore directory: %w", err)
		}
	}

	verified := VerifiedCommit{Document: commit, ETag: opened.Info.ETag}
	defer func() {
		if resultErr != nil {
			_ = verified.Close()
		}
	}()
	for _, segment := range commit.Segments {
		prefix := strings.TrimSuffix(segment.ManifestKey, "/manifest.json")
		segmentRestore := ""
		if restoreDir != "" {
			segmentRestore = filepath.Join(restoreDir, "segments", safePathPart(segment.SegmentID))
			if err := os.MkdirAll(filepath.Dir(segmentRestore), 0o700); err != nil {
				return VerifiedCommit{}, err
			}
		}
		item, err := VerifySegment(ctx, store, SegmentDescriptor{
			Prefix: prefix, ManifestKey: segment.ManifestKey, ManifestSHA256: segment.ManifestSHA256,
		}, segmentRestore)
		if err != nil {
			return VerifiedCommit{}, err
		}
		if item.Manifest.SegmentID != segment.SegmentID || item.Manifest.SegmentKind != segment.SegmentKind ||
			!item.Manifest.WindowStart.Equal(commit.WindowStart) || !item.Manifest.WindowEnd.Equal(commit.WindowEnd) {
			_ = item.Close()
			return VerifiedCommit{}, corruptArtifact("commit.json", fmt.Errorf("segment descriptor mismatch"))
		}
		verified.RecordCount += item.Manifest.RecordCount
		verified.BlobRefCount += item.Manifest.BlobRefCount
		verified.BlobPresentCount += item.Manifest.BlobPresentCount
		verified.BlobMissingCount += item.Manifest.BlobMissingCount
		verified.Segments = append(verified.Segments, item)
	}
	if err := verifyCommitIdentities(verified.Segments); err != nil {
		return VerifiedCommit{}, corruptArtifact("commit.json", err)
	}
	if err := verifyCommitAggregate(commit, verified); err != nil {
		return VerifiedCommit{}, err
	}
	return verified, nil
}

func VerifySegment(ctx context.Context, store ObjectStore, descriptor SegmentDescriptor, restoreDir string) (_ VerifiedSegment, resultErr error) {
	manifestBytes, err := readObjectBounded(ctx, store, descriptor.ManifestKey, maxManifestBytes)
	if err != nil {
		return VerifiedSegment{}, corruptArtifact("manifest.json", err)
	}
	if descriptor.ManifestSHA256 != "" && SHA256Hex(manifestBytes) != descriptor.ManifestSHA256 {
		return VerifiedSegment{}, corruptArtifact("manifest.json", fmt.Errorf("checksum mismatch"))
	}
	var manifest SegmentManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return VerifiedSegment{}, corruptArtifact("manifest.json", err)
	}
	if err := validateManifestDescriptor(manifest, descriptor); err != nil {
		return VerifiedSegment{}, err
	}
	if manifest.BlobMissingCount != 0 {
		return VerifiedSegment{}, &IntegrityError{
			Code: IntegrityMissingEvidence,
			Err:  fmt.Errorf("manifest declares %d missing evidence references", manifest.BlobMissingCount),
		}
	}

	workDir, scratch, err := verificationDirectory(restoreDir)
	if err != nil {
		return VerifiedSegment{}, err
	}
	defer func() {
		if resultErr != nil && scratch != "" {
			_ = os.RemoveAll(scratch)
		}
	}()

	recordsPath := filepath.Join(workDir, "records.parquet")
	recordsSize, err := downloadObject(ctx, store, descriptor.Prefix+"/records.parquet", recordsPath, manifest.RecordsSHA256)
	if err != nil {
		return VerifiedSegment{}, corruptArtifact("records.parquet", err)
	}
	identityPath := filepath.Join(workDir, "record-identities.jsonl")
	evidenceRefsPath := filepath.Join(workDir, "expected-evidence-refs.jsonl")
	identityCount, blobRefs, err := verifyParquetIdentities(recordsPath, identityPath, evidenceRefsPath)
	if err != nil {
		return VerifiedSegment{}, corruptArtifact("records.parquet", err)
	}
	if identityCount != manifest.RecordCount || blobRefs != manifest.BlobRefCount {
		return VerifiedSegment{}, corruptArtifact("records.parquet", fmt.Errorf(
			"count mismatch records=%d/%d blob_refs=%d/%d",
			identityCount, manifest.RecordCount, blobRefs, manifest.BlobRefCount,
		))
	}

	var evidenceSize, indexSize int64
	if manifest.BlobRefCount > 0 {
		if manifest.EvidencePackSHA256 == "" || manifest.EvidenceIndexSHA256 == "" {
			return VerifiedSegment{}, corruptArtifact("manifest.json", fmt.Errorf("evidence checksums are required"))
		}
		evidencePath := filepath.Join(workDir, "evidence.pack")
		evidenceSize, err = downloadObject(ctx, store, descriptor.Prefix+"/evidence.pack", evidencePath, manifest.EvidencePackSHA256)
		if err != nil {
			return VerifiedSegment{}, corruptArtifact("evidence.pack", err)
		}
		indexPath := filepath.Join(workDir, "evidence-index.jsonl.zst")
		indexSize, err = downloadObject(ctx, store, descriptor.Prefix+"/evidence-index.jsonl.zst", indexPath, manifest.EvidenceIndexSHA256)
		if err != nil {
			return VerifiedSegment{}, corruptArtifact("evidence-index.jsonl.zst", err)
		}
		indexCount, err := verifyEvidenceIndex(indexPath, evidencePath, evidenceRefsPath, evidenceSize, restoreDir)
		if err != nil {
			return VerifiedSegment{}, corruptArtifact("evidence-index.jsonl.zst", err)
		}
		if indexCount != manifest.BlobRefCount || indexCount != manifest.BlobPresentCount {
			return VerifiedSegment{}, corruptArtifact("evidence-index.jsonl.zst", fmt.Errorf(
				"count mismatch index=%d refs=%d present=%d",
				indexCount, manifest.BlobRefCount, manifest.BlobPresentCount,
			))
		}
	} else if manifest.BlobPresentCount != 0 || manifest.EvidencePackSHA256 != "" || manifest.EvidenceIndexSHA256 != "" {
		return VerifiedSegment{}, corruptArtifact("manifest.json", fmt.Errorf("zero-reference segment declares evidence artifacts"))
	}
	if recordsSize+evidenceSize+indexSize != manifest.ArtifactBytes {
		return VerifiedSegment{}, corruptArtifact("manifest.json", fmt.Errorf(
			"artifact bytes mismatch actual=%d manifest=%d",
			recordsSize+evidenceSize+indexSize, manifest.ArtifactBytes,
		))
	}

	return VerifiedSegment{
		Manifest: manifest, IdentityPath: identityPath, IdentityCount: identityCount,
		RestoreDir: restoreDir, scratchDir: scratch,
	}, nil
}

func validateCommitDocument(commit CommitDocument) error {
	if commit.SchemaVersion != CommitSchemaV1 && commit.SchemaVersion != CommitSchemaV2 {
		return corruptArtifact("commit.json", fmt.Errorf("unsupported schema %q", commit.SchemaVersion))
	}
	if !commit.WindowEnd.Equal(commit.WindowStart.Add(time.Hour)) || len(commit.Segments) == 0 {
		return corruptArtifact("commit.json", fmt.Errorf("invalid window or empty segment set"))
	}
	seen := make(map[string]struct{}, len(commit.Segments))
	for index, segment := range commit.Segments {
		if segment.SegmentID == "" || segment.ManifestKey == "" || segment.ManifestSHA256 == "" {
			return corruptArtifact("commit.json", fmt.Errorf("incomplete segment descriptor"))
		}
		if _, exists := seen[segment.SegmentID]; exists {
			return corruptArtifact("commit.json", fmt.Errorf("duplicate segment id"))
		}
		seen[segment.SegmentID] = struct{}{}
		if index == 0 && segment.SegmentKind != SegmentKindBase {
			return corruptArtifact("commit.json", fmt.Errorf("first segment must be base"))
		}
		if index > 0 && segment.SegmentKind != SegmentKindDelta {
			return corruptArtifact("commit.json", fmt.Errorf("later segments must be delta"))
		}
	}
	return nil
}

func validateCommitLocation(commitKey string, commit CommitDocument) error {
	shardPrefix := ShardRelativePrefix(commit.WindowStart)
	if commitKey != shardPrefix+"/commit.json" {
		return corruptArtifact("commit.json", fmt.Errorf("object key window mismatch"))
	}
	for _, segment := range commit.Segments {
		expected := shardPrefix + "/segments/" + segment.SegmentID + "/manifest.json"
		if segment.ManifestKey != expected {
			return corruptArtifact("commit.json", fmt.Errorf("manifest key outside commit shard"))
		}
	}
	return nil
}

func CommitAggregateSHA256(segments []CommitSegment) (string, error) {
	canonical, err := MarshalJSON(segments)
	if err != nil {
		return "", err
	}
	return SHA256Hex(canonical), nil
}

func verifyCommitAggregate(commit CommitDocument, verified VerifiedCommit) error {
	if commit.SchemaVersion == CommitSchemaV1 {
		if len(commit.Segments) != 1 || commit.AggregateSHA256 != commit.Segments[0].ManifestSHA256 {
			return corruptArtifact("commit.json", fmt.Errorf("invalid legacy aggregate"))
		}
		return nil
	}
	aggregate, err := CommitAggregateSHA256(commit.Segments)
	if err != nil {
		return corruptArtifact("commit.json", err)
	}
	if aggregate != commit.AggregateSHA256 ||
		commit.AggregateRecordCount != verified.RecordCount ||
		commit.AggregateBlobRefCount != verified.BlobRefCount ||
		commit.AggregateBlobPresentCount != verified.BlobPresentCount ||
		commit.AggregateBlobMissingCount != verified.BlobMissingCount {
		return corruptArtifact("commit.json", fmt.Errorf("aggregate mismatch"))
	}
	return nil
}

func validateManifestDescriptor(manifest SegmentManifest, descriptor SegmentDescriptor) error {
	if manifest.SchemaVersion != ManifestSchemaV1 {
		return corruptArtifact("manifest.json", fmt.Errorf("unsupported schema %q", manifest.SchemaVersion))
	}
	if manifest.SegmentID == "" || (manifest.SegmentKind != SegmentKindBase && manifest.SegmentKind != SegmentKindDelta) {
		return corruptArtifact("manifest.json", fmt.Errorf("invalid segment identity"))
	}
	if !manifest.WindowEnd.After(manifest.WindowStart) || !manifest.WindowEnd.Equal(manifest.WindowStart.Add(time.Hour)) {
		return corruptArtifact("manifest.json", fmt.Errorf("invalid window"))
	}
	if filepath.Base(descriptor.Prefix) != manifest.SegmentID {
		return corruptArtifact("manifest.json", fmt.Errorf("segment prefix mismatch"))
	}
	if descriptor.ManifestKey != descriptor.Prefix+"/manifest.json" {
		return corruptArtifact("manifest.json", fmt.Errorf("manifest key mismatch"))
	}
	return nil
}

func verificationDirectory(restoreDir string) (workDir string, scratch string, err error) {
	if restoreDir != "" {
		if err := os.Mkdir(restoreDir, 0o700); err != nil {
			return "", "", fmt.Errorf("create restore directory: %w", err)
		}
		if err := os.Chmod(restoreDir, 0o700); err != nil {
			return "", "", fmt.Errorf("secure restore directory: %w", err)
		}
		return restoreDir, "", nil
	}
	scratch, err = os.MkdirTemp("", "qa-archive-verify-")
	if err != nil {
		return "", "", err
	}
	if err := os.Chmod(scratch, 0o700); err != nil {
		_ = os.RemoveAll(scratch)
		return "", "", err
	}
	return scratch, scratch, nil
}

func readObjectBounded(ctx context.Context, store ObjectStore, key string, limit int64) ([]byte, error) {
	opened, err := store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.Body.Close() }()
	if opened.Info.Size > limit {
		return nil, fmt.Errorf("object exceeds %d byte limit", limit)
	}
	body, err := io.ReadAll(io.LimitReader(opened.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit || (opened.Info.Size >= 0 && int64(len(body)) != opened.Info.Size) {
		return nil, fmt.Errorf("object size mismatch")
	}
	return body, nil
}

func downloadObject(ctx context.Context, store ObjectStore, key, destination, expectedSHA256 string) (int64, error) {
	if expectedSHA256 == "" {
		return 0, fmt.Errorf("expected checksum is required")
	}
	opened, err := store.Open(ctx, key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = opened.Body.Close() }()
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, digest), opened.Body)
	if err != nil {
		return 0, err
	}
	if opened.Info.Size >= 0 && size != opened.Info.Size {
		return 0, fmt.Errorf("size mismatch read=%d head=%d", size, opened.Info.Size)
	}
	if hex.EncodeToString(digest.Sum(nil)) != expectedSHA256 {
		return 0, fmt.Errorf("checksum mismatch")
	}
	if err := file.Sync(); err != nil {
		return 0, err
	}
	return size, file.Close()
}

func verifyParquetIdentities(recordsPath, identityPath, evidenceRefsPath string) (recordCount int64, blobRefCount int64, err error) {
	records, err := os.Open(recordsPath)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = records.Close() }()
	reader := parquet.NewGenericReader[RecordRow](records)
	defer func() { _ = reader.Close() }()
	identities, err := os.OpenFile(identityPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = identities.Close() }()
	expectedRefs, err := os.OpenFile(evidenceRefsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = expectedRefs.Close() }()
	writer := bufio.NewWriter(identities)
	refsWriter := bufio.NewWriter(expectedRefs)
	page := make([]RecordRow, verifyRowPageSize)
	var previous RecordIdentity
	for {
		n, readErr := reader.Read(page)
		for _, row := range page[:n] {
			identity := RecordIdentity{CreatedAt: time.UnixMicro(row.CreatedAt).UTC(), RequestID: row.RequestID}
			if identity.RequestID == "" {
				return 0, 0, fmt.Errorf("empty request id")
			}
			if recordCount > 0 && compareIdentity(previous, identity) >= 0 {
				return 0, 0, fmt.Errorf("duplicate or unordered record identity")
			}
			encoded, marshalErr := json.Marshal(identity)
			if marshalErr != nil {
				return 0, 0, marshalErr
			}
			if _, writeErr := writer.Write(append(encoded, '\n')); writeErr != nil {
				return 0, 0, writeErr
			}
			for _, ref := range orderedBlobRefs(row) {
				if ref.URI == nil || strings.TrimSpace(*ref.URI) == "" {
					continue
				}
				expected, marshalErr := json.Marshal(evidenceIndexLine{
					RequestID: row.RequestID, BlobField: ref.Field, BlobURI: *ref.URI,
				})
				if marshalErr != nil {
					return 0, 0, marshalErr
				}
				if _, writeErr := refsWriter.Write(append(expected, '\n')); writeErr != nil {
					return 0, 0, writeErr
				}
				blobRefCount++
			}
			recordCount++
			previous = identity
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, 0, readErr
		}
	}
	if err := writer.Flush(); err != nil {
		return 0, 0, err
	}
	if err := refsWriter.Flush(); err != nil {
		return 0, 0, err
	}
	if err := identities.Sync(); err != nil {
		return 0, 0, err
	}
	if err := expectedRefs.Sync(); err != nil {
		return 0, 0, err
	}
	if err := identities.Close(); err != nil {
		return 0, 0, err
	}
	return recordCount, blobRefCount, expectedRefs.Close()
}

func compareIdentity(left, right RecordIdentity) int {
	if left.CreatedAt.Before(right.CreatedAt) {
		return -1
	}
	if left.CreatedAt.After(right.CreatedAt) {
		return 1
	}
	return strings.Compare(left.RequestID, right.RequestID)
}

func verifyEvidenceIndex(indexPath, evidencePath, expectedRefsPath string, evidenceSize int64, restoreRoot string) (int64, error) {
	indexFile, err := os.Open(indexPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = indexFile.Close() }()
	decoder, err := zstd.NewReader(indexFile)
	if err != nil {
		return 0, err
	}
	defer decoder.Close()
	pack, err := os.Open(evidencePath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = pack.Close() }()
	expectedRefs, err := os.Open(expectedRefsPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = expectedRefs.Close() }()

	scanner := bufio.NewScanner(decoder)
	expectedScanner := bufio.NewScanner(expectedRefs)
	expectedScanner.Buffer(make([]byte, 64*1024), maxManifestBytes)
	scanner.Buffer(make([]byte, 64*1024), maxManifestBytes)
	var count int64
	var previousEnd int64
	for scanner.Scan() {
		var line evidenceIndexLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return 0, err
		}
		if line.RequestID == "" || line.BlobField == "" || line.Offset < 0 || line.Length < 0 ||
			line.Offset != previousEnd || line.Offset+line.Length > evidenceSize {
			return 0, fmt.Errorf("invalid evidence range")
		}
		if !expectedScanner.Scan() {
			return 0, fmt.Errorf("evidence index has unexpected reference")
		}
		var expected evidenceIndexLine
		if err := json.Unmarshal(expectedScanner.Bytes(), &expected); err != nil {
			return 0, err
		}
		if line.RequestID != expected.RequestID || line.BlobField != expected.BlobField || line.BlobURI != expected.BlobURI {
			return 0, fmt.Errorf("evidence index does not match parquet")
		}
		section := io.NewSectionReader(pack, line.Offset, line.Length)
		digest := sha256.New()
		var destination io.Writer = digest
		var output *os.File
		if restoreRoot != "" {
			directory := filepath.Join(restoreRoot, "evidence", safePathPart(line.RequestID))
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return 0, err
			}
			output, err = os.OpenFile(filepath.Join(directory, safePathPart(line.BlobField)+".bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return 0, err
			}
			destination = io.MultiWriter(digest, output)
		}
		written, copyErr := io.Copy(destination, section)
		if output != nil {
			closeErr := output.Close()
			if copyErr == nil {
				copyErr = closeErr
			}
		}
		if copyErr != nil || written != line.Length || hex.EncodeToString(digest.Sum(nil)) != line.SHA256 {
			return 0, fmt.Errorf("evidence checksum or length mismatch")
		}
		previousEnd = line.Offset + line.Length
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if expectedScanner.Scan() {
		return 0, fmt.Errorf("evidence index is missing parquet reference")
	}
	if err := expectedScanner.Err(); err != nil {
		return 0, err
	}
	if previousEnd != evidenceSize {
		return 0, fmt.Errorf("evidence pack has unindexed bytes")
	}
	return count, nil
}

var safePathPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type identityStream struct {
	index   int
	file    *os.File
	scanner *bufio.Scanner
	current RecordIdentity
}

type identityHeap []*identityStream

func (h identityHeap) Len() int           { return len(h) }
func (h identityHeap) Less(i, j int) bool { return compareIdentity(h[i].current, h[j].current) < 0 }
func (h identityHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *identityHeap) Push(value any)    { *h = append(*h, value.(*identityStream)) }
func (h *identityHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}

func verifyCommitIdentities(segments []VerifiedSegment) error {
	streams := make([]*identityStream, 0, len(segments))
	defer func() {
		for _, stream := range streams {
			_ = stream.file.Close()
		}
	}()
	queue := identityHeap{}
	for index, segment := range segments {
		file, err := os.Open(segment.IdentityPath)
		if err != nil {
			return err
		}
		stream := &identityStream{index: index, file: file, scanner: bufio.NewScanner(file)}
		stream.scanner.Buffer(make([]byte, 16*1024), maxManifestBytes)
		streams = append(streams, stream)
		if stream.scanner.Scan() {
			if err := json.Unmarshal(stream.scanner.Bytes(), &stream.current); err != nil {
				return err
			}
			heap.Push(&queue, stream)
		} else if err := stream.scanner.Err(); err != nil {
			return err
		}
	}
	heap.Init(&queue)
	var previous RecordIdentity
	havePrevious := false
	for queue.Len() > 0 {
		stream := heap.Pop(&queue).(*identityStream)
		if havePrevious && compareIdentity(previous, stream.current) == 0 {
			return fmt.Errorf("duplicate record identity across segments")
		}
		previous, havePrevious = stream.current, true
		if stream.scanner.Scan() {
			if err := json.Unmarshal(stream.scanner.Bytes(), &stream.current); err != nil {
				return err
			}
			heap.Push(&queue, stream)
		} else if err := stream.scanner.Err(); err != nil {
			return err
		}
	}
	return nil
}

func safePathPart(value string) string {
	if safePathPattern.MatchString(value) && value != "." && value != ".." {
		return value
	}
	return SHA256Hex([]byte(value))
}

func corruptArtifact(field string, err error) *IntegrityError {
	return &IntegrityError{Code: IntegrityCorruptArtifact, BlobField: field, Err: err}
}
