//go:build unit

package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestS3ManagedBatchObjectStoreStreamsAndScopesKeys(t *testing.T) {
	var uploaded []byte
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		require.Equal(t, "/batch-bucket/managed-batch/input.jsonl", r.URL.Path)
		switch r.Method {
		case http.MethodPut:
			var err error
			uploaded, err = io.ReadAll(r.Body)
			require.NoError(t, err)
		case http.MethodGet:
			_, _ = w.Write([]byte("result"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := newS3Client(context.Background(), s3ClientParams{
		Endpoint: server.URL, Region: "ap-guangzhou", AccessKeyID: "test-ak", SecretAccessKey: "test-sk", ForcePathStyle: true,
	})
	require.NoError(t, err)
	store := &S3ManagedBatchObjectStore{client: client, bucket: "batch-bucket", prefix: "managed-batch"}

	size, err := store.Upload(context.Background(), "input.jsonl", strings.NewReader("input"), "application/jsonl")
	require.NoError(t, err)
	require.Equal(t, int64(5), size)
	require.Equal(t, []byte("input"), uploaded)

	body, err := store.Download(context.Background(), "input.jsonl")
	require.NoError(t, err)
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, []byte("result"), got)

	require.NoError(t, store.Delete(context.Background(), "input.jsonl"))
	require.Equal(t, []string{http.MethodPut, http.MethodGet, http.MethodDelete}, methods)
}
