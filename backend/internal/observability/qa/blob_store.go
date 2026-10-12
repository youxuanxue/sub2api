package qa

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type BlobStore interface {
	Put(ctx context.Context, key string, body []byte, contentType string) (string, error)
	// PutReader streams body to the store without buffering the whole payload
	// in memory. A seekable reader lets the S3 driver set Content-Length.
	PutReader(ctx context.Context, key string, r io.Reader, contentType string) (string, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

type localFSBlobStore struct {
	root string
}

func newLocalFSBlobStore(root string) BlobStore {
	return &localFSBlobStore{root: root}
}

func (s *localFSBlobStore) Put(_ context.Context, key string, body []byte, _ string) (string, error) {
	fullPath, err := trajectory.WriteBlobFile(s.root, key, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	return "file://" + fullPath, nil
}

func (s *localFSBlobStore) PutReader(_ context.Context, key string, r io.Reader, _ string) (string, error) {
	fullPath, err := trajectory.WriteBlobFile(s.root, key, r)
	if err != nil {
		return "", err
	}
	return "file://" + fullPath, nil
}

func (s *localFSBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	dir, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return dir.ReadFile(filepath.FromSlash(key))
}

func (s *localFSBlobStore) Delete(_ context.Context, key string) error {
	target := key
	if filepath.IsAbs(target) {
		var err error
		target, err = filepath.Rel(s.root, target)
		if err != nil {
			return err
		}
	}
	dir, err := os.OpenRoot(s.root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Remove(filepath.FromSlash(target)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), qaS3LoadOptions(cfg.Storage)...)
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

// qaS3LoadOptions builds the AWS SDK config load options for the QA S3 store.
//
// It injects a static credentials provider ONLY when an access key is explicitly
// configured (non-AWS / local S3-compatible stores like R2 / MinIO). When the
// access key is empty it adds NO credentials provider, so LoadDefaultConfig uses
// the default AWS credential chain. Production capture uses local blobs; raw
// archive coordinates and credential mounts belong to the independent maintenance
// runtime (docs/approved/qa-raw-only.md), not to a user export bucket.
//
// Previously newBlobStore unconditionally passed an empty static provider, so
// S3-backed writes failed with "operation error S3: PutObject ... static
// credentials are empty". This mirrors S3MediaStore
// (internal/repository/media_s3_store.go), which is why media offload works on
// the same credential chain.
func qaS3LoadOptions(storage config.QACaptureStorageConfig) []func(*awsconfig.LoadOptions) error {
	region := strings.TrimSpace(storage.Region)
	if region == "" {
		region = "auto" // Cloudflare R2 default
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if strings.TrimSpace(storage.AccessKeyID) != "" || strings.TrimSpace(storage.CredentialsFile) != "" {
		opts = append(opts, func(o *awsconfig.LoadOptions) error {
			access, secret, err := storage.ReadCredentials()
			if err != nil {
				return err
			}
			o.Credentials = credentials.NewStaticCredentialsProvider(access, secret, "")
			return nil
		})
	}
	return opts
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
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		return "", err
	}
	return "s3://" + s.bucket + "/" + fullKey, nil
}

func (s *s3BlobStore) PutReader(ctx context.Context, key string, r io.Reader, contentType string) (string, error) {
	fullKey := s.fullKey(key)
	in := &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &fullKey,
		Body:        r,
		ContentType: &contentType,
		IfNoneMatch: aws.String("*"),
	}
	// A ReadSeeker (e.g. *os.File) lets the SDK compute Content-Length without
	// buffering; non-seekable readers fall back to the SDK's own handling.
	if rs, ok := r.(io.ReadSeeker); ok {
		if size, err := rs.Seek(0, io.SeekEnd); err == nil {
			if _, err := rs.Seek(0, io.SeekStart); err == nil {
				in.ContentLength = &size
			}
		}
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
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
	defer func() {
		_ = out.Body.Close()
	}()
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
