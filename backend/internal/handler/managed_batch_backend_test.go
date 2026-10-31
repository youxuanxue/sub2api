package handler

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/stretchr/testify/require"
)

type managedBatchStoreFake struct{ objects map[string][]byte }

func (s *managedBatchStoreFake) Upload(_ context.Context, key string, r io.Reader, _ string) (int64, error) {
	b, e := io.ReadAll(r)
	if e == nil {
		s.objects[key] = b
	}
	return int64(len(b)), e
}
func (s *managedBatchStoreFake) Download(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.objects[key])), nil
}
func (s *managedBatchStoreFake) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

type managedBatchRepoFake struct {
	files       map[string]repository.ManagedBatchFile
	batches     map[string]repository.ManagedBatch
	submissions int
}

func (r *managedBatchRepoFake) CreateFile(_ context.Context, f repository.ManagedBatchFile) error {
	r.files[f.ID] = f
	return nil
}
func (r *managedBatchRepoFake) GetFileForOwner(_ context.Context, u, k int64, id string) (*repository.ManagedBatchFile, error) {
	f, ok := r.files[id]
	if !ok || f.UserID != u || (f.APIKeyID != 0 && f.APIKeyID != k) {
		return nil, io.EOF
	}
	return &f, nil
}
func (r *managedBatchRepoFake) DeleteFileForOwner(_ context.Context, u, k int64, id string) error {
	f, e := r.GetFileForOwner(context.Background(), u, k, id)
	if e != nil {
		return e
	}
	delete(r.files, f.ID)
	return nil
}
func (r *managedBatchRepoFake) CreateBatchSubmission(_ context.Context, s repository.ManagedBatchSubmission) error {
	r.batches[s.Batch.ID] = s.Batch
	r.submissions++
	return nil
}
func (r *managedBatchRepoFake) GetBatchForOwner(_ context.Context, u, k int64, id string) (*repository.ManagedBatch, error) {
	b, ok := r.batches[id]
	if !ok || b.UserID != u || (b.APIKeyID != 0 && b.APIKeyID != k) {
		return nil, io.EOF
	}
	return &b, nil
}
func (r *managedBatchRepoFake) ListBatchesForOwner(_ context.Context, u, k int64, _ string, limit int) ([]repository.ManagedBatch, bool, error) {
	o := []repository.ManagedBatch{}
	for _, b := range r.batches {
		if b.UserID == u && (b.APIKeyID == 0 || b.APIKeyID == k) {
			o = append(o, b)
		}
	}
	return o, false, nil
}
func (r *managedBatchRepoFake) CompareAndSwapBatchStatus(_ context.Context, u, k int64, id string, v int64, status string, _ *time.Time, _ *string, _ *string) (bool, error) {
	b, e := r.GetBatchForOwner(context.Background(), u, k, id)
	if e != nil || b.Version != v {
		return false, e
	}
	b.Status = status
	b.Version++
	r.batches[id] = *b
	return true, nil
}
func (r *managedBatchRepoFake) EnqueueOutbox(context.Context, repository.ManagedBatchOutboxEvent) error {
	return nil
}

func TestManagedBatchRepositoryBackendCreateBatchPersistsValidatedMapper(t *testing.T) {
	repo := &managedBatchRepoFake{files: map[string]repository.ManagedBatchFile{}, batches: map[string]repository.ManagedBatch{}}
	store := &managedBatchStoreFake{objects: map[string][]byte{}}
	b := NewManagedBatchRepositoryBackend(repo, store, nil)
	owner := ManagedBatchOwner{UserID: 7, APIKeyID: 9}
	got, err := b.CreateFile(context.Background(), owner, "input.jsonl", "batch", 0, bytes.NewBufferString("line\n"))
	require.NoError(t, err)
	require.Len(t, store.objects, 1)
	require.Equal(t, "batch", got.Object.Purpose)
	line := batchcontract.RequestLine{CustomID: "row-1", Method: "POST", URL: batchcontract.EndpointResponses, Body: []byte(`{"model":"gpt"}`)}
	batch, err := b.CreateBatch(context.Background(), owner, batchcontract.BatchSubmitRequest{InputFileID: got.Object.ID, Endpoint: batchcontract.EndpointResponses, CompletionWindow: "24h"}, []batchcontract.RequestLine{line}, "idem-1", "hash")
	require.NoError(t, err)
	require.Equal(t, batchcontract.BatchStatusValidating, batch.Status)
	require.Equal(t, 1, repo.submissions)
}

func TestManagedBatchRepositoryBackendMissingOwnerDoesNotOpenObject(t *testing.T) {
	repo := &managedBatchRepoFake{files: map[string]repository.ManagedBatchFile{}, batches: map[string]repository.ManagedBatch{}}
	b := NewManagedBatchRepositoryBackend(repo, &managedBatchStoreFake{objects: map[string][]byte{}}, nil)
	_, err := b.GetFile(context.Background(), ManagedBatchOwner{UserID: 2, APIKeyID: 3}, "file_missing")
	require.Error(t, err)
}
