package batchworker

import (
	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ProvideManagedBatchRuntime wires the first managed rollout. The resolver
// and runner re-enter the existing OpenAI Responses path; the adapter map is
// intentionally limited to OpenAI until the later provider conversions land.
// Runtime.Start is safe while disabled and has no goroutine or network side
// effect in the default configuration.
func ProvideManagedBatchRuntime(
	repo *repository.ManagedBatchRepository,
	store *repository.S3ManagedBatchObjectStore,
	queue service.ManagedBatchQueue,
	cursor service.ManagedBatchOutboxCursor,
	apiKeys *service.APIKeyService,
	gateway *service.OpenAIGatewayService,
	router *protocolrouter.Router,
	cfg *config.Config,
) *service.ManagedBatchRuntime {
	if router == nil {
		return service.NewManagedBatchRuntime(nil, nil, queue, false)
	}
	resolver := &service.ManagedBatchExecutionResolver{APIKeys: apiKeys, Gateway: gateway, Router: router}
	runner := &service.ManagedResponsesRunner{Router: router, Gateway: gateway}
	finalizer := &repository.ManagedBatchResultFinalizer{Repo: repo, Store: store}
	enabled := cfg != nil && cfg.ManagedBatch.Enabled && store != nil
	settlement := &service.OpenAIManagedBatchSettlement{Gateway: gateway, APIKeyService: apiKeys}
	processor := NewProcessor(Config{
		Enabled:         enabled,
		WorkerID:        "managed-batch-worker",
		Resolver:        resolver,
		ResultFinalizer: finalizer,
		Settlement:      settlement,
	}, repo, map[string]batchadapter.ManagedExecutor{
		batchadapter.ProviderOpenAI: batchadapter.NewOpenAIAdapter(runner),
	})
	worker := service.NewManagedBatchWorker(queue, processor, service.ManagedBatchWorkerOptions{})
	publisher := &service.ManagedBatchOutboxPublisher{Outbox: repo, Cursor: cursor, Queue: queue}
	runtime := service.NewManagedBatchRuntime(worker, publisher, queue, enabled)
	runtime.Start()
	return runtime
}
