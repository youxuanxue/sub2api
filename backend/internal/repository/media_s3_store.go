package repository

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// S3MediaStore implements service.MediaStore over an S3(-compatible) bucket.
//
// Mirrors S3BackupStore, with ONE deliberate difference: when AccessKeyID is
// empty it does NOT inject a static credentials provider, so LoadDefaultConfig
// uses the default AWS credential chain — i.e. the EC2 instance role on prod.
// This is the whole point of the design decision: media offload introduces NO
// long-lived AWS key; presigned links are signed by the instance role and are
// therefore short-lived (re-minted on demand by VideoFetch).
type S3MediaStore struct {
	client *s3.Client
	bucket string
}

// NewMediaStore builds the media store from config. Returns nil (offload
// disabled) when no driver/bucket is configured — the gateway then passes media
// through as inline base64 (current behaviour). A nil return is a valid wired
// value: handlers nil-check the MediaStore.
func NewMediaStore(cfg *config.Config) service.MediaStore {
	mc := cfg.MediaStorage
	if mc.Driver == "" || mc.Bucket == "" {
		logger.L().Info("media_store.disabled",
			zap.String("reason", "media_storage.driver/bucket not set — inline base64 passthrough"))
		return nil
	}

	region := mc.Region
	if region == "" {
		region = "auto" // Cloudflare R2 default
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	// Static creds ONLY when explicitly provided (non-AWS / local S3). On prod
	// both are empty → default chain → EC2 instance role (no long-lived key).
	if mc.AccessKeyID != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(mc.AccessKeyID, mc.SecretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		// Misconfiguration must not crash startup; fall back to passthrough.
		logger.L().Error("media_store.load_config_failed",
			zap.String("reason", "falling back to inline base64 passthrough"), zap.Error(err))
		return nil
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if mc.Endpoint != "" {
			o.BaseEndpoint = &mc.Endpoint
		}
		if mc.ForcePathStyle {
			o.UsePathStyle = true
		}
		o.APIOptions = append(o.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	logger.L().Info("media_store.enabled",
		zap.String("driver", mc.Driver), zap.String("bucket", mc.Bucket), zap.String("region", region),
		zap.Bool("static_creds", mc.AccessKeyID != ""))
	return &S3MediaStore{client: client, bucket: mc.Bucket}
}

func (s *S3MediaStore) Upload(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(body),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("media S3 PutObject: %w", err)
	}
	return nil
}

func (s *S3MediaStore) PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	result, err := s3.NewPresignClient(s.client).PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("media presign url: %w", err)
	}
	return result.URL, nil
}
