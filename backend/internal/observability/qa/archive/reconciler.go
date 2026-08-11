package archive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultCommitCASAttempts  = 3
	failurePersistenceTimeout = 5 * time.Second
	defaultSourceRetention    = 24 * time.Hour
)

type Window struct {
	Start time.Time
	End   time.Time
}

type ReconcileReceipt struct {
	WindowStart        time.Time `json:"window_start"`
	WindowEnd          time.Time `json:"window_end"`
	CommitKey          string    `json:"commit_key"`
	CommitETag         string    `json:"commit_etag"`
	SegmentCount       int       `json:"segment_count"`
	RecordCount        int64     `json:"record_count"`
	BlobRefCount       int64     `json:"blob_ref_count"`
	BlobPresentCount   int64     `json:"blob_present_count"`
	BlobMissingCount   int64     `json:"blob_missing_count"`
	Uploaded           bool      `json:"uploaded"`
	DeletionAuthorized bool      `json:"deletion_authorized"`
}

type ReconcileControl interface {
	EnsureShard(context.Context, *sql.Conn, Window) (int64, error)
	ImportCommit(context.Context, *sql.Conn, int64, VerifiedCommit) error
	OrphanIncomplete(context.Context, *sql.Conn, int64) error
	PendingVerified(context.Context, *sql.Conn, int64) ([]CommitSegment, error)
	StartSegment(context.Context, *sql.Conn, int64, BuiltSegment, string) (int64, error)
	MarkSegmentVerified(context.Context, *sql.Conn, int64, VerifiedSegment) error
	FailSegment(context.Context, *sql.Conn, int64, string, error) error
	PersistCommit(context.Context, *sql.Conn, int64, VerifiedCommit) error
	Fail(context.Context, *sql.Conn, int64, string, error) error
}

type Reconciler struct {
	Store           ObjectStore
	Control         ReconcileControl
	ScratchRoot     string
	BlobRoot        string
	PageSize        int
	CASAttempts     int
	SourceRetention time.Duration
	Now             func() time.Time
	Build           func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error)
	VerifyOne       func(context.Context, ObjectStore, SegmentDescriptor, string) (VerifiedSegment, error)
	VerifyAll       func(context.Context, ObjectStore, string, string) (VerifiedCommit, error)
}

func NewReconciler(store ObjectStore, control ReconcileControl, scratchRoot string) *Reconciler {
	return &Reconciler{
		Store: store, Control: control, ScratchRoot: scratchRoot,
		CASAttempts: defaultCommitCASAttempts, SourceRetention: defaultSourceRetention, Now: time.Now,
		Build: BuildSegment,
		VerifyOne: func(ctx context.Context, store ObjectStore, descriptor SegmentDescriptor, restoreDir string) (VerifiedSegment, error) {
			return VerifySegment(ctx, store, descriptor, restoreDir)
		},
		VerifyAll: func(ctx context.Context, store ObjectStore, commitKey, restoreDir string) (VerifiedCommit, error) {
			return VerifyCommit(ctx, store, commitKey, restoreDir)
		},
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, conn *sql.Conn, window Window) (_ ReconcileReceipt, resultErr error) {
	window.Start = window.Start.UTC()
	window.End = window.End.UTC()
	if !window.End.Equal(window.Start.Add(time.Hour)) {
		return ReconcileReceipt{}, fmt.Errorf("reconcile window must be one UTC hour")
	}
	if r.Store == nil || r.Control == nil || r.Build == nil || r.VerifyOne == nil || r.VerifyAll == nil {
		return ReconcileReceipt{}, fmt.Errorf("reconciler dependencies are incomplete")
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.CASAttempts <= 0 {
		r.CASAttempts = defaultCommitCASAttempts
	}
	if r.SourceRetention <= 0 {
		r.SourceRetention = defaultSourceRetention
	}

	shardID, err := r.Control.EnsureShard(ctx, conn, window)
	if err != nil {
		return ReconcileReceipt{}, fmt.Errorf("ensure archive shard: %w", err)
	}
	defer func() {
		if resultErr == nil {
			return
		}
		failureCtx, cancel := context.WithTimeout(context.Background(), failurePersistenceTimeout)
		defer cancel()
		_ = r.Control.Fail(failureCtx, conn, shardID, reconcileErrorCode(resultErr), resultErr)
	}()

	commitKey := ShardRelativePrefix(window.Start) + "/commit.json"
	current, exists, err := r.readCommit(ctx, commitKey)
	if err != nil {
		return ReconcileReceipt{}, err
	}
	if exists {
		defer func() { _ = current.Close() }()
		if err := r.Control.ImportCommit(ctx, conn, shardID, current); err != nil {
			return ReconcileReceipt{}, fmt.Errorf("import existing commit control: %w", err)
		}
	}

	if err := r.Control.OrphanIncomplete(ctx, conn, shardID); err != nil {
		return ReconcileReceipt{}, fmt.Errorf("orphan interrupted archive segments: %w", err)
	}
	uploaded := false
	pending, err := r.Control.PendingVerified(ctx, conn, shardID)
	if err != nil {
		return ReconcileReceipt{}, fmt.Errorf("load verified segments: %w", err)
	}
	if len(pending) == 0 {
		kind := SegmentKindBase
		if exists {
			kind = SegmentKindDelta
		}
		built, err := r.Build(ctx, conn, BuildInput{
			WindowStart: window.Start, WindowEnd: window.End, SegmentKind: kind,
			BlobRoot: r.BlobRoot, ScratchRoot: r.ScratchRoot, PageSize: r.PageSize,
		})
		if err != nil {
			return ReconcileReceipt{}, err
		}
		defer func() { _ = built.Close() }()
		if built.Manifest.RecordCount == 0 {
			if exists {
				if err := r.Control.PersistCommit(ctx, conn, shardID, current); err != nil {
					return ReconcileReceipt{}, fmt.Errorf("persist verified existing commit: %w", err)
				}
				return receiptFromVerified(commitKey, current), nil
			}
			if window.Start.Before(r.Now().UTC().Add(-r.SourceRetention)) {
				return ReconcileReceipt{}, &IntegrityError{
					Code: IntegritySourceUnavailableAfterRetention,
					Err:  fmt.Errorf("source window is older than the retention boundary"),
				}
			}
		}

		prefix := ShardRelativePrefix(window.Start) + "/segments/" + built.SegmentID
		segmentDBID, err := r.Control.StartSegment(ctx, conn, shardID, built, prefix)
		if err != nil {
			return ReconcileReceipt{}, fmt.Errorf("start archive segment: %w", err)
		}
		segmentVerified := false
		defer func() {
			if resultErr == nil || segmentVerified {
				return
			}
			failureCtx, cancel := context.WithTimeout(context.Background(), failurePersistenceTimeout)
			defer cancel()
			_ = r.Control.FailSegment(failureCtx, conn, segmentDBID, reconcileErrorCode(resultErr), resultErr)
		}()
		if err := uploadBuiltSegment(ctx, r.Store, prefix, built); err != nil {
			return ReconcileReceipt{}, err
		}
		uploaded = true
		manifestArtifact, ok := builtArtifact(built, "manifest.json")
		if !ok {
			return ReconcileReceipt{}, fmt.Errorf("built segment has no manifest")
		}
		verified, err := r.VerifyOne(ctx, r.Store, SegmentDescriptor{
			Prefix: prefix, ManifestKey: prefix + "/manifest.json", ManifestSHA256: manifestArtifact.SHA256,
		}, "")
		if err != nil {
			return ReconcileReceipt{}, err
		}
		if err := r.Control.MarkSegmentVerified(ctx, conn, segmentDBID, verified); err != nil {
			_ = verified.Close()
			return ReconcileReceipt{}, fmt.Errorf("persist verified segment: %w", err)
		}
		_ = verified.Close()
		segmentVerified = true
		pending = append(pending, CommitSegment{
			SegmentID: built.SegmentID, SegmentKind: built.Manifest.SegmentKind,
			ManifestKey: prefix + "/manifest.json", ManifestSHA256: manifestArtifact.SHA256,
		})
	}

	verified, err := r.commitPending(ctx, commitKey, window, pending)
	if err != nil {
		return ReconcileReceipt{}, err
	}
	defer func() { _ = verified.Close() }()
	if err := r.Control.PersistCommit(ctx, conn, shardID, verified); err != nil {
		return ReconcileReceipt{}, fmt.Errorf("persist committed archive state: %w", err)
	}
	receipt := receiptFromVerified(commitKey, verified)
	receipt.Uploaded = uploaded
	return receipt, nil
}

func (r *Reconciler) readCommit(ctx context.Context, commitKey string) (VerifiedCommit, bool, error) {
	opened, err := r.Store.Open(ctx, commitKey)
	if err != nil {
		if isObjectNotFound(err) {
			return VerifiedCommit{}, false, nil
		}
		return VerifiedCommit{}, false, fmt.Errorf("open archive commit: %w", err)
	}
	if closeErr := opened.Body.Close(); closeErr != nil {
		return VerifiedCommit{}, false, fmt.Errorf("close archive commit body: %w", closeErr)
	}
	verified, err := r.VerifyAll(ctx, r.Store, commitKey, "")
	if err != nil {
		return VerifiedCommit{}, true, err
	}
	return verified, true, nil
}

func isObjectNotFound(err error) bool {
	return isObjectStoreNotFound(err)
}

func (r *Reconciler) commitPending(ctx context.Context, commitKey string, window Window, pending []CommitSegment) (VerifiedCommit, error) {
	for attempt := 0; attempt < r.CASAttempts; attempt++ {
		current, exists, err := r.readCommit(ctx, commitKey)
		if err != nil {
			return VerifiedCommit{}, err
		}
		segments := make([]CommitSegment, 0, len(pending)+len(current.Document.Segments))
		if exists {
			segments = append(segments, current.Document.Segments...)
		}
		known := make(map[string]struct{}, len(segments))
		for _, segment := range segments {
			known[segment.SegmentID] = struct{}{}
		}
		for _, segment := range pending {
			if _, ok := known[segment.SegmentID]; ok {
				continue
			}
			segments = append(segments, segment)
			known[segment.SegmentID] = struct{}{}
		}
		_ = current.Close()
		if len(segments) == 0 {
			return VerifiedCommit{}, fmt.Errorf("no verified archive segments to commit")
		}

		counts, err := r.verifyDescriptors(ctx, segments)
		if err != nil {
			return VerifiedCommit{}, err
		}
		aggregate, err := CommitAggregateSHA256(segments)
		if err != nil {
			return VerifiedCommit{}, err
		}
		document := CommitDocument{
			SchemaVersion: CommitSchemaV2, WindowStart: window.Start, WindowEnd: window.End,
			Generation: 0, Segments: segments, AggregateSHA256: aggregate,
			AggregateRecordCount: counts.RecordCount, AggregateBlobRefCount: counts.BlobRefCount,
			AggregateBlobPresentCount: counts.BlobPresentCount, AggregateBlobMissingCount: counts.BlobMissingCount,
			CommittedAt: r.Now().UTC(),
		}
		body, err := MarshalJSON(document)
		if err != nil {
			return VerifiedCommit{}, err
		}
		if exists {
			_, err = r.Store.CompareAndSwap(ctx, commitKey, current.ETag, bytes.NewReader(body), int64(len(body)), "application/json")
		} else {
			_, err = r.Store.Create(ctx, commitKey, bytes.NewReader(body), int64(len(body)), "application/json")
		}
		if errors.Is(err, ErrPreconditionFailed) {
			continue
		}
		if err != nil {
			return VerifiedCommit{}, fmt.Errorf("write archive commit: %w", err)
		}
		verified, err := r.VerifyAll(ctx, r.Store, commitKey, "")
		if err != nil {
			return VerifiedCommit{}, fmt.Errorf("verify committed archive: %w", err)
		}
		return verified, nil
	}
	return VerifiedCommit{}, fmt.Errorf("archive commit CAS retries exhausted")
}

func (r *Reconciler) verifyDescriptors(ctx context.Context, segments []CommitSegment) (VerifiedCommit, error) {
	var aggregate VerifiedCommit
	for _, segment := range segments {
		verified, err := r.VerifyOne(ctx, r.Store, SegmentDescriptor{
			Prefix:      strings.TrimSuffix(segment.ManifestKey, "/manifest.json"),
			ManifestKey: segment.ManifestKey, ManifestSHA256: segment.ManifestSHA256,
		}, "")
		if err != nil {
			_ = aggregate.Close()
			return VerifiedCommit{}, err
		}
		aggregate.RecordCount += verified.Manifest.RecordCount
		aggregate.BlobRefCount += verified.Manifest.BlobRefCount
		aggregate.BlobPresentCount += verified.Manifest.BlobPresentCount
		aggregate.BlobMissingCount += verified.Manifest.BlobMissingCount
		aggregate.Segments = append(aggregate.Segments, verified)
	}
	if err := verifyCommitIdentities(aggregate.Segments); err != nil {
		_ = aggregate.Close()
		return VerifiedCommit{}, corruptArtifact("commit.json", err)
	}
	_ = aggregate.Close()
	return aggregate, nil
}

func uploadBuiltSegment(ctx context.Context, store ObjectStore, prefix string, built BuiltSegment) error {
	for _, artifact := range built.Artifacts {
		file, err := os.Open(artifact.Path)
		if err != nil {
			return fmt.Errorf("open segment artifact %s: %w", artifact.Name, err)
		}
		_, putErr := store.PutReader(ctx, prefix+"/"+artifact.Name, file, artifact.Size, artifact.ContentType)
		closeErr := file.Close()
		if putErr != nil {
			return fmt.Errorf("create segment artifact %s: %w", artifact.Name, putErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close segment artifact %s: %w", artifact.Name, closeErr)
		}
	}
	return nil
}

func builtArtifact(built BuiltSegment, name string) (BuiltArtifact, bool) {
	for _, artifact := range built.Artifacts {
		if artifact.Name == name {
			return artifact, true
		}
	}
	return BuiltArtifact{}, false
}

func receiptFromVerified(commitKey string, verified VerifiedCommit) ReconcileReceipt {
	return ReconcileReceipt{
		WindowStart: verified.Document.WindowStart, WindowEnd: verified.Document.WindowEnd,
		CommitKey: commitKey, CommitETag: verified.ETag, SegmentCount: len(verified.Document.Segments),
		RecordCount: verified.RecordCount, BlobRefCount: verified.BlobRefCount,
		BlobPresentCount: verified.BlobPresentCount, BlobMissingCount: verified.BlobMissingCount,
		DeletionAuthorized: false,
	}
}

func reconcileErrorCode(err error) string {
	var integrity *IntegrityError
	if errors.As(err, &integrity) && integrity.Code != "" {
		return integrity.Code
	}
	if errors.Is(err, ErrPreconditionFailed) {
		return "commit_conflict"
	}
	return "archive_failed"
}
