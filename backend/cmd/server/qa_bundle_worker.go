package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/bundle"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type qaBundleWorkerDeps struct {
	loadConfig func() (*config.Config, error)
	newWorker  func(context.Context, *config.Config) (bundle.Worker, error)
}

func qaBundleWorkerRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--qa-bundle-worker" || arg == "--qa-bundle-worker=true" ||
			arg == "--qa-bundle-worker-once" || arg == "--qa-bundle-worker-once=true" {
			return true
		}
	}
	return false
}

func defaultQABundleWorkerDeps() qaBundleWorkerDeps {
	return qaBundleWorkerDeps{loadConfig: config.LoadForBootstrap, newWorker: newQABundleWorker}
}

func runQABundleWorkerCommand(ctx context.Context, args []string, _ io.Writer, deps qaBundleWorkerDeps) error {
	fs := flag.NewFlagSet("qa-bundle-worker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var workerMode, once bool
	fs.BoolVar(&workerMode, "qa-bundle-worker", false, "run the QA Bundle SQS worker")
	fs.BoolVar(&once, "qa-bundle-worker-once", false, "process at most one QA Bundle SQS delivery")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if workerMode == once || fs.NArg() != 0 {
		return errors.New("exactly one qa bundle worker mode is required")
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		return err
	}
	if !cfg.QaBundle.Enabled || !cfg.QaArchive.Enabled {
		return errors.New("qa bundle and raw archive must both be enabled")
	}
	worker, err := deps.newWorker(ctx, cfg)
	if err != nil {
		return err
	}
	if once {
		_, err := worker.RunOne(ctx)
		return err
	}
	for ctx.Err() == nil {
		if _, err := worker.RunOne(ctx); err != nil && ctx.Err() == nil {
			log.Printf("QA bundle worker delivery failed: %v", err)
		}
	}
	return nil
}

func newQABundleWorker(ctx context.Context, cfg *config.Config) (bundle.Worker, error) {
	rawStore, err := archive.NewObjectStoreFromConfig(ctx, cfg.QaArchive.Storage)
	if err != nil {
		return bundle.Worker{}, fmt.Errorf("configure raw QA archive reader: %w", err)
	}
	outputObjectStore, err := archive.NewObjectStoreFromConfig(ctx, cfg.QaBundle.Storage)
	if err != nil {
		return bundle.Worker{}, fmt.Errorf("configure QA bundle output store: %w", err)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, qaBundleAWSLoadOptions(cfg.QaBundle.Storage)...)
	if err != nil {
		return bundle.Worker{}, fmt.Errorf("configure QA bundle SQS client: %w", err)
	}
	queue, err := bundle.NewSQSJobQueue(sqs.NewFromConfig(awsCfg), cfg.QaBundle.QueueURL)
	if err != nil {
		return bundle.Worker{}, err
	}
	restoreRoot := strings.TrimSpace(os.Getenv("QA_BUNDLE_RESTORE_ROOT"))
	if restoreRoot == "" {
		restoreRoot = os.TempDir()
	}
	return bundle.Worker{
		Consumer: queue, RawStore: rawStore, OutputStore: bundle.NewArchiveStore(outputObjectStore), RestoreRoot: restoreRoot,
	}, nil
}

func qaBundleAWSLoadOptions(storage config.QACaptureStorageConfig) []func(*awsconfig.LoadOptions) error {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(strings.TrimSpace(storage.Region))}
	if strings.TrimSpace(storage.AccessKeyID) != "" {
		options = append(options, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(storage.AccessKeyID, storage.SecretAccessKey, ""),
		))
	}
	return options
}
