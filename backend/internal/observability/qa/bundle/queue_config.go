package bundle

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type ManagedJobQueue interface {
	JobQueue
	JobConsumer
	io.Closer
}

// OpenJobQueue is shared by the producer, worker, and release canary.
// The PostgreSQL path never loads AWS credentials or uses the application DB.
func OpenJobQueue(ctx context.Context, cfg config.QaBundleConfig) (ManagedJobQueue, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.QueueDriver)) {
	case "postgres":
		return openPostgresJobQueue(ctx, cfg.QueuePostgresDSNFile)
	case "", "sqs":
		opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(strings.TrimSpace(cfg.Storage.Region))}
		if strings.TrimSpace(cfg.Storage.AccessKeyID) != "" {
			opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.Storage.AccessKeyID, cfg.Storage.SecretAccessKey, "")))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return nil, errors.New("configure QA SQS credentials")
		}
		return NewSQSJobQueue(sqs.NewFromConfig(awsCfg), cfg.QueueURL)
	default:
		return nil, errors.New("unsupported QA queue driver")
	}
}

func (q *SQSJobQueue) Close() error { return nil }
