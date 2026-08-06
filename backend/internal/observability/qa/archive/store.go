package archive

import (
	"bytes"
	"context"
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

// ObjectStore uploads immutable raw-archive objects (design §8.1).
type ObjectStore interface {
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

// NewObjectStoreFromConfig builds the raw archive S3 client. Empty static credentials
// defer to the instance role on prod (same pattern as qa blob_store).
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

func (s *s3ObjectStore) Put(ctx context.Context, key string, body []byte, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.fullKey(key)),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	}
	_, err := s.client.PutObject(ctx, input)
	return err
}

func (s *s3ObjectStore) PutIfAbsent(ctx context.Context, key string, body []byte, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.fullKey(key)),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
		IfNoneMatch: aws.String("*"),
	}
	_, err := s.client.PutObject(ctx, input)
	if err != nil && stringsContains(err.Error(), "PreconditionFailed", "412") {
		return fmt.Errorf("qa archive object already exists: %s", key)
	}
	return err
}

func (s *s3ObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()
	return io.ReadAll(out.Body)
}

func (s *s3ObjectStore) Head(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if stringsContains(err.Error(), "NotFound", "404") {
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
