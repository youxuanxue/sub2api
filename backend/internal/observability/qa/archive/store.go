package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var ErrPreconditionFailed = errors.New("qa archive object precondition failed")

type ObjectInfo struct {
	ETag string
	Size int64
}

type ObjectReader struct {
	Info ObjectInfo
	Body io.ReadCloser
}

// ObjectStore provides immutable artifact writes and ETag-guarded commit updates.
type ObjectStore interface {
	PutReader(ctx context.Context, key string, body io.Reader, size int64, contentType string) (ObjectInfo, error)
	Create(ctx context.Context, key string, body io.Reader, size int64, contentType string) (ObjectInfo, error)
	CompareAndSwap(ctx context.Context, key, expectedETag string, body io.Reader, size int64, contentType string) (ObjectInfo, error)
	Open(ctx context.Context, key string) (ObjectReader, error)
	HeadInfo(ctx context.Context, key string) (ObjectInfo, error)

	// Transitional byte helpers keep Phase 2b callers source-compatible while the
	// segment builder moves artifact bodies to files/readers.
	Put(ctx context.Context, key string, body []byte, contentType string) error
	PutIfAbsent(ctx context.Context, key string, body []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Head(ctx context.Context, key string) (bool, error)
}

type s3ObjectStore struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewObjectStoreFromConfig(ctx context.Context, storage config.QACaptureStorageConfig) (ObjectStore, error) {
	driver := strings.ToLower(strings.TrimSpace(storage.Driver))
	if driver != "s3" {
		return nil, fmt.Errorf("qa archive storage driver must be s3, got %q", driver)
	}
	bucket := strings.TrimSpace(storage.Bucket)
	if bucket == "" {
		return nil, fmt.Errorf("qa archive storage bucket is required")
	}
	region := strings.TrimSpace(storage.Region)
	if region == "" {
		return nil, fmt.Errorf("qa archive storage region is required")
	}

	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if strings.TrimSpace(storage.AccessKeyID) != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(storage.AccessKeyID, storage.SecretAccessKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load qa archive aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint := strings.TrimSpace(storage.Endpoint); endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
		if storage.ForcePathStyle {
			o.UsePathStyle = true
		}
		o.APIOptions = append(o.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	prefix := strings.Trim(strings.TrimSpace(storage.Prefix), "/")
	return &s3ObjectStore{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *s3ObjectStore) fullKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *s3ObjectStore) put(
	ctx context.Context,
	key string,
	body io.Reader,
	size int64,
	contentType string,
	ifMatch *string,
	ifNoneMatch *string,
) (ObjectInfo, error) {
	if size < 0 {
		return ObjectInfo{}, fmt.Errorf("qa archive object size must be non-negative")
	}
	out, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(s.fullKey(key)),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
		IfMatch:       ifMatch,
		IfNoneMatch:   ifNoneMatch,
	})
	if err != nil {
		if stringsContains(err.Error(), "PreconditionFailed", "ConditionalRequestConflict", "412", "409") {
			return ObjectInfo{}, fmt.Errorf("%w: %s", ErrPreconditionFailed, key)
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{ETag: aws.ToString(out.ETag), Size: size}, nil
}

func (s *s3ObjectStore) PutReader(ctx context.Context, key string, body io.Reader, size int64, contentType string) (ObjectInfo, error) {
	return s.put(ctx, key, body, size, contentType, nil, nil)
}

func (s *s3ObjectStore) Create(ctx context.Context, key string, body io.Reader, size int64, contentType string) (ObjectInfo, error) {
	return s.put(ctx, key, body, size, contentType, nil, aws.String("*"))
}

func (s *s3ObjectStore) CompareAndSwap(ctx context.Context, key, expectedETag string, body io.Reader, size int64, contentType string) (ObjectInfo, error) {
	if strings.TrimSpace(expectedETag) == "" {
		return ObjectInfo{}, fmt.Errorf("expected ETag is required")
	}
	return s.put(ctx, key, body, size, contentType, aws.String(expectedETag), nil)
}

func (s *s3ObjectStore) Open(ctx context.Context, key string) (ObjectReader, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		return ObjectReader{}, err
	}
	return ObjectReader{
		Info: ObjectInfo{ETag: aws.ToString(out.ETag), Size: aws.ToInt64(out.ContentLength)},
		Body: out.Body,
	}, nil
}

func (s *s3ObjectStore) HeadInfo(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{ETag: aws.ToString(out.ETag), Size: aws.ToInt64(out.ContentLength)}, nil
}

func (s *s3ObjectStore) Put(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := s.PutReader(ctx, key, bytes.NewReader(body), int64(len(body)), contentType)
	return err
}

func (s *s3ObjectStore) PutIfAbsent(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := s.Create(ctx, key, bytes.NewReader(body), int64(len(body)), contentType)
	return err
}

func (s *s3ObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	opened, err := s.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.Body.Close() }()
	return io.ReadAll(opened.Body)
}

func (s *s3ObjectStore) Head(ctx context.Context, key string) (bool, error) {
	_, err := s.HeadInfo(ctx, key)
	if err != nil {
		if stringsContains(err.Error(), "NotFound", "404", "NoSuchKey") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func stringsContains(msg string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(msg, part) {
			return true
		}
	}
	return false
}
