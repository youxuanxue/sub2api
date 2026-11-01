package repository

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3ManagedBatchObjectStore stores private Batch input and result JSONL in an
// S3-compatible bucket. Tencent COS is the production target; the same
// adapter also works with MinIO and other S3-compatible test stores.
type S3ManagedBatchObjectStore struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewManagedBatchObjectStore returns nil until the bucket and credentials are
// configured. The Batch handler treats nil as unavailable and remains closed.
func NewManagedBatchObjectStore(cfg *config.Config) *S3ManagedBatchObjectStore {
	if cfg == nil {
		return nil
	}
	sc := cfg.ManagedBatch.Storage
	if strings.TrimSpace(sc.Driver) == "" || strings.TrimSpace(sc.Bucket) == "" || strings.TrimSpace(sc.AccessKeyID) == "" || strings.TrimSpace(sc.SecretAccessKey) == "" {
		return nil
	}
	client, err := newS3Client(context.Background(), s3ClientParams{
		Endpoint:        sc.Endpoint,
		Region:          sc.Region,
		AccessKeyID:     sc.AccessKeyID,
		SecretAccessKey: sc.SecretAccessKey,
		ForcePathStyle:  sc.ForcePathStyle,
	})
	if err != nil {
		return nil
	}
	return &S3ManagedBatchObjectStore{
		client: client,
		bucket: sc.Bucket,
		prefix: strings.Trim(strings.TrimSpace(sc.Prefix), "/"),
	}
}

func (s *S3ManagedBatchObjectStore) objectKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *S3ManagedBatchObjectStore) Upload(ctx context.Context, key string, body io.Reader, contentType string) (int64, error) {
	if s == nil || s.client == nil || body == nil {
		return 0, fmt.Errorf("managed batch object store is unavailable")
	}
	counted := &countingReader{Reader: body}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         managedBatchStringPtr(s.objectKey(key)),
		Body:        counted,
		ContentType: managedBatchStringPtr(contentType),
	})
	if err != nil {
		return 0, fmt.Errorf("managed batch COS PutObject: %w", err)
	}
	return counted.n, nil
}

func (s *S3ManagedBatchObjectStore) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("managed batch object store is unavailable")
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: managedBatchStringPtr(s.objectKey(key))})
	if err != nil {
		return nil, fmt.Errorf("managed batch COS GetObject: %w", err)
	}
	return result.Body, nil
}

func (s *S3ManagedBatchObjectStore) Delete(ctx context.Context, key string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("managed batch object store is unavailable")
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: managedBatchStringPtr(s.objectKey(key))})
	if err != nil {
		return fmt.Errorf("managed batch COS DeleteObject: %w", err)
	}
	return nil
}

func managedBatchStringPtr(value string) *string { return &value }

type countingReader struct {
	io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += int64(n)
	return n, err
}
