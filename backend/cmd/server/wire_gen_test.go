package main

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvideServiceBuildInfo(t *testing.T) {
	in := handler.BuildInfo{
		Version:   "v-test",
		BuildType: "release",
	}
	out := provideServiceBuildInfo(in)
	require.Equal(t, in.Version, out.Version)
	require.Equal(t, in.BuildType, out.BuildType)
}

func TestProvideCleanup_WithMinimalDependencies_NoPanic(t *testing.T) {
	cfg := &config.Config{}

	oauthSvc := service.NewOAuthService(nil, nil)
	openAIOAuthSvc := service.NewOpenAIOAuthService(nil, nil)
	geminiOAuthSvc := service.NewGeminiOAuthService(nil, nil, nil, nil, cfg)
	antigravityOAuthSvc := service.NewAntigravityOAuthService(nil)

	tokenRefreshSvc := service.NewTokenRefreshService(
		nil,
		oauthSvc,
		openAIOAuthSvc,
		geminiOAuthSvc,
		antigravityOAuthSvc,
		nil,
		nil,
		cfg,
		nil,
	)
	accountExpirySvc := service.NewAccountExpiryService(nil, time.Second)
	codexVersionSyncSvc := service.NewOpenAICodexVersionSyncService(nil, nil, nil, time.Second)
	proxyExpirySvc := service.NewProxyExpiryService(nil, time.Second)
	subscriptionExpirySvc := service.NewSubscriptionExpiryService(nil, time.Second)
	pricingSvc := service.NewPricingService(cfg, nil)
	emailQueueSvc := service.NewEmailQueueService(nil, 1)
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	idempotencyCleanupSvc := service.NewIdempotencyCleanupService(nil, cfg)
	schedulerSnapshotSvc := service.NewSchedulerSnapshotService(nil, nil, nil, nil, cfg)
	// TK fix for upstream Wei-Shaw/sub2api#2538: nil repo makes Start() a no-op
	// so this test does not spin a goroutine even though provideCleanup wires
	// the Stop() call.
	schedulerRateLimitReaperSvc := service.NewSchedulerRateLimitReaper(nil, cfg)
	opsSystemLogSinkSvc := service.NewOpsSystemLogSink(nil)
	tkHooks := provideTKCleanupHooks(
		schedulerRateLimitReaperSvc,
		nil, // anthropicConfigReconciler
		nil, // holdReconciler
		nil, // accountIncidentNotifier
		nil, // pricingMissingNotifier
		service.TKAuthServiceColdStartReady{},
		service.TKGatewayPricingAvailabilityReady{},
		service.TKPricingOverlayRuntimeReady{},
		service.TKAccountModelMappingRuntimeServingReady{},
		service.TKGatewayAnthropicSigPreemptReady{},
		service.TKAnthropicSaturationReady{},
		service.TKOpenAISaturationReady{},
		service.TKAntigravitySaturationReady{},
		handler.TKGatewayHandlerModelListReady{},
		service.TKUniversalModelsProviderReady{},
		service.TKGroupUnsupportedModelCacheReady{},
		service.ProtocolRoutingSSOTReady{},
	)

	cleanup := provideCleanup(
		nil, // entClient
		nil, // redis
		&service.OpsMetricsCollector{},
		&service.OpsAggregationService{},
		&service.OpsAlertEvaluatorService{},
		&service.OpsCleanupService{},
		&service.OpsScheduledReportService{},
		opsSystemLogSinkSvc,
		nil, // opsService
		nil, // opsIngressRejectAggregator
		nil, // apiKeyService
		nil, // authCacheInvalidationWorker
		schedulerSnapshotSvc,
		nil, // upstreamBalanceSentinel
		tokenRefreshSvc,
		accountExpirySvc,
		nil, // cnProviderBalanceCheck
		codexVersionSyncSvc,
		proxyExpirySvc,
		subscriptionExpirySvc,
		&service.UsageCleanupService{},
		idempotencyCleanupSvc,
		nil, // batchImageCleanup
		nil, // batchImageWorker
		pricingSvc,
		emailQueueSvc,
		billingCacheSvc,
		&service.UsageRecordWorkerPool{},
		nil, // qaCapture
		&service.SubscriptionService{},
		oauthSvc,
		openAIOAuthSvc,
		geminiOAuthSvc,
		antigravityOAuthSvc,
		nil, // grokOAuth
		nil, // openAIGateway
		nil, // scheduledTestRunner
		nil, // backupSvc
		nil, // paymentOrderExpiry
		nil, // channelMonitorRunner
		nil, // channelMonitorV2Aggregator
		nil, // terminalOutcomeRecorder
		tkHooks,
		nil, // quotaFlusher
		nil, // upstreamBillingProbe
		nil, // ollamaCloudUsage
		nil, // auditLog
		nil, // promptAudit
		nil, // telemetryArchive
		nil, // telemetryArchiveHealth
	)

	require.NotPanics(t, func() {
		cleanup()
	})
}
