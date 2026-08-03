package telemetryarchive

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3PutClient interface {
	PutObject(ctx context.Context, input *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type s3Uploader struct {
	client s3PutClient
}

func (u *s3Uploader) PutObject(ctx context.Context, request PutRequest) error {
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(request.Bucket),
		Key:                  aws.String(request.Key),
		Body:                 bytes.NewReader(request.Body),
		ContentType:          aws.String(request.ContentType),
		ContentEncoding:      aws.String(request.ContentEncoding),
		Metadata:             request.Metadata,
		ServerSideEncryption: "AES256",
	})
	return err
}

func NewS3(ctx context.Context, region string, config Config) *Shadow {
	region = strings.TrimSpace(region)
	config.Bucket = strings.TrimSpace(config.Bucket)
	if !config.Enabled {
		return New(config, nil)
	}
	if region == "" || config.Bucket == "" || strings.TrimSpace(config.Prefix) == "" {
		slog.Warn("telemetry archive configuration incomplete; shadow disabled")
		config.Enabled = false
		return New(config, nil)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		slog.Warn("telemetry archive AWS config unavailable; shadow disabled", "err", err)
		config.Enabled = false
		return New(config, nil)
	}
	return New(config, &s3Uploader{client: s3.NewFromConfig(awsConfig)})
}

func ConfigFromValues(enabled bool, bucket, prefix string, queueSize, batchSize, flushSeconds, putTimeoutSeconds int) Config {
	return Config{
		Enabled:       enabled,
		Bucket:        bucket,
		Prefix:        prefix,
		QueueSize:     queueSize,
		BatchSize:     batchSize,
		FlushInterval: time.Duration(flushSeconds) * time.Second,
		PutTimeout:    time.Duration(putTimeoutSeconds) * time.Second,
	}
}
