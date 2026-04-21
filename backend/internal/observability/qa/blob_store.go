package qa

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type BlobStore interface {
	Put(ctx context.Context, key string, body []byte, contentType string) (string, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error)
}

type localFSBlobStore struct {
	root string
}

func newLocalFSBlobStore(root string) BlobStore {
	return &localFSBlobStore{root: root}
}

func (s *localFSBlobStore) Put(_ context.Context, key string, body []byte, _ string) (string, error) {
	fullPath := filepath.Join(s.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(fullPath, body, 0o644); err != nil {
		return "", err
	}
	return "file://" + fullPath, nil
}

func (s *localFSBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.root, filepath.FromSlash(key)))
}

func (s *localFSBlobStore) Delete(_ context.Context, key string) error {
	target := key
	if !filepath.IsAbs(target) {
		target = filepath.Join(s.root, filepath.FromSlash(key))
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *localFSBlobStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "file://" + filepath.Join(s.root, filepath.FromSlash(key)), nil
}

type s3BlobStore struct {
	client *s3.Client
	bucket string
	prefix string
}

func newBlobStore(cfg config.QACaptureConfig) (BlobStore, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Storage.Driver))
	if driver == "" || driver == "localfs" {
		dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
		if dataDir == "" {
			dataDir = "/app/data"
		}
		return newLocalFSBlobStore(filepath.Join(dataDir, "qa_blobs")), nil
	}

	region := strings.TrimSpace(cfg.Storage.Region)
	if region == "" {
		region = "auto"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.Storage.AccessKeyID, cfg.Storage.SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("load qa s3 config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint := strings.TrimSpace(cfg.Storage.Endpoint); endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
		if cfg.Storage.ForcePathStyle {
			o.UsePathStyle = true
		}
		o.APIOptions = append(o.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	return &s3BlobStore{
		client: client,
		bucket: cfg.Storage.Bucket,
		prefix: strings.Trim(strings.TrimSpace(cfg.Storage.Prefix), "/"),
	}, nil
}

func (s *s3BlobStore) fullKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *s3BlobStore) Put(ctx context.Context, key string, body []byte, contentType string) (string, error) {
	fullKey := s.fullKey(key)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &fullKey,
		Body:        bytes.NewReader(body),
		ContentType: &contentType,
	})
	if err != nil {
		return "", err
	}
	return "s3://" + s.bucket + "/" + fullKey, nil
}

func (s *s3BlobStore) Get(ctx context.Context, key string) ([]byte, error) {
	fullKey := s.fullKey(key)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &fullKey,
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *s3BlobStore) Delete(ctx context.Context, key string) error {
	fullKey := s.fullKey(key)
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &fullKey,
	})
	return err
}

func (s *s3BlobStore) PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	fullKey := s.fullKey(key)
	client := s3.NewPresignClient(s.client)
	out, err := client.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &fullKey,
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}
