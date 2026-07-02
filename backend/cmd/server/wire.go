//go:build wireinject
// +build wireinject

package main

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	qaobs "github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

type Application struct {
	Server  *http.Server
	Cleanup func()
}

func initializeApplication(buildInfo handler.BuildInfo) (*Application, error) {
	wire.Build(
		// Infrastructure layer ProviderSets
		config.ProviderSet,

		// Business layer ProviderSets
		repository.ProviderSet,
		service.ProviderSet,
		wire.NewSet(qaobs.NewService),
		payment.ProviderSet,
		middleware.ProviderSet,
		handler.ProviderSet,

		// Server layer ProviderSet
		server.ProviderSet,

		// Privacy client factory for OpenAI training opt-out
		providePrivacyClientFactory,

		// BuildInfo provider
		provideServiceBuildInfo,

		// Cleanup function provider
		provideCleanup,

		// Application struct
		wire.Struct(new(Application), "Server", "Cleanup"),
	)
	return nil, nil
}

func providePrivacyClientFactory() service.PrivacyClientFactory {
	return repository.CreatePrivacyReqClient
}

func provideServiceBuildInfo(buildInfo handler.BuildInfo) service.BuildInfo {
	return service.BuildInfo{
		Version:   buildInfo.Version,
		BuildType: buildInfo.BuildType,
	}
}

func provideCleanup(
	entClient *ent.Client,
	rdb *redis.Client,
	opsMetricsCollector *service.OpsMetricsCollector,
	opsAggregation *service.OpsAggregationService,
	opsAlertEvaluator *service.OpsAlertEvaluatorService,
	opsCleanup *service.OpsCleanupService,
	opsScheduledReport *service.OpsScheduledReportService,
	opsSystemLogSink *service.OpsSystemLogSink,
	schedulerSnapshot *service.SchedulerSnapshotService,
	// TK fix for upstream Wei-Shaw/sub2api#2538 — see
	// internal/service/scheduler_rate_limit_reaper.go.
	schedulerRateLimitReaper *service.SchedulerRateLimitReaper,
	// TK: per-node anthropic config self-healer — see
	// internal/service/anthropic_config_reconciler.go.
	anthropicConfigReconciler *service.AnthropicConfigReconciler,
	// TK: per-node antigravity config self-healer (gemini-only model_mapping) —
	// see internal/service/antigravity_config_reconciler.go.
	antigravityConfigReconciler *service.AntigravityConfigReconciler,
	upstreamBalanceSentinel *service.UpstreamBalanceSentinel,
	tokenRefresh *service.TokenRefreshService,
	accountExpiry *service.AccountExpiryService,
	proxyExpiry *service.ProxyExpiryService,
	subscriptionExpiry *service.SubscriptionExpiryService,
	usageCleanup *service.UsageCleanupService,
	idempotencyCleanup *service.IdempotencyCleanupService,
	// TokenKey: pre-flight balance-hold reconciler — passed so its sweep ticker
	// is Stopped at shutdown and so wire forces evaluation of
	// ProvideHoldReconcilerService (which starts the ticker at construction).
	holdReconciler *service.HoldReconcilerService,
	pricing *service.PricingService,
	emailQueue *service.EmailQueueService,
	billingCache *service.BillingCacheService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	qaCapture *qaobs.Service,
	subscriptionService *service.SubscriptionService,
	oauth *service.OAuthService,
	openaiOAuth *service.OpenAIOAuthService,
	geminiOAuth *service.GeminiOAuthService,
	antigravityOAuth *service.AntigravityOAuthService,
	grokOAuth *service.GrokOAuthService,
	openAIGateway *service.OpenAIGatewayService,
	scheduledTestRunner *service.ScheduledTestRunnerService,
	backupSvc *service.BackupService,
	paymentOrderExpiry *service.PaymentOrderExpiryService,
	channelMonitorRunner *service.ChannelMonitorRunner,
	// TokenKey: account-incident Feishu notifier. Passed so its digest ticker is
	// Stopped at shutdown, and so wire forces evaluation of
	// ProvideTKAccountIncidentNotifier (which attaches it onto RateLimitService).
	accountIncidentNotifier *service.TKAccountIncidentNotifier,
	// TokenKey: pricing-missing Feishu notifier. Passed so its digest ticker is
	// Stopped at shutdown, and so wire forces evaluation of
	// ProvideTKPricingMissingNotifier (which attaches it onto both gateways).
	pricingMissingNotifier *service.TKPricingMissingNotifier,
	// TokenKey: forces wire to evaluate ProvideTKAuthServiceColdStart so the
	// trial-key issuer gets wired onto AuthService at startup. The value is
	// unused — only the dependency edge matters. See US-029 / US-030.
	_ service.TKAuthServiceColdStartReady,
	// TokenKey: forces wire to evaluate ProvideTKGatewayPricingAvailability so
	// GatewayService.SetPricingAvailabilityService is called at startup. The
	// handler-side wiring is forced via ProvideTKPricingCatalogHandler being
	// the constructor used by handler ProviderSet. See R-001 of
	// docs/approved/pricing-availability-source-of-truth.md.
	_ service.TKGatewayPricingAvailabilityReady,
	// TokenKey: forces wire to evaluate ProvideTKPricingOverlayRuntime so the
	// runtime hot-pushable pricing overlay (settings-blob getter + catalog cache
	// invalidator + pub/sub subscribe) is wired onto PricingService at startup.
	// Without this edge wire would dead-code the post-construction setter.
	_ service.TKPricingOverlayRuntimeReady,
	// TokenKey: forces wire to evaluate ProvideTKGatewayAnthropicSigPreempt so
	// GatewayService.SetAnthropicSigPreemptCache is called at startup. Without
	// this dependency edge wire would dead-code the post-construction setter
	// because no other production component references the sentinel.
	_ service.TKGatewayAnthropicSigPreemptReady,
	// TokenKey: forces wire to evaluate ProvideTKAnthropicSaturation so the
	// saturation counter is wired onto GatewayService + RateLimitService at
	// startup (otherwise wire dead-codes the post-construction setters).
	_ service.TKAnthropicSaturationReady,
	// TokenKey: forces wire to evaluate ProvideTKGatewayHandlerModelList so
	// GatewayHandler.SetModelListFilter is called at startup. See R-003 /
	// Goal 2 of docs/approved/pricing-availability-source-of-truth.md.
	_ handler.TKGatewayHandlerModelListReady,
	// TokenKey: forces wire to evaluate ProvideTKUniversalModelsProvider so the
	// universal-key resolver's "group served-model set" truth source
	// (GatewayService.GetAvailableModels) is wired onto APIKeyService at startup.
	// Without this edge wire dead-codes the post-construction setter and the
	// resolver silently falls back to platform-level routing. See
	// docs/approved/universal-key-routing.md.
	_ service.TKUniversalModelsProviderReady,
	// TokenKey: forces wire to evaluate ProvideTKGroupUnsupportedModelCache so
	// the shared selection-time unsupported-model negative cache is wired at startup.
	_ service.TKGroupUnsupportedModelCacheReady,
) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		type cleanupStep struct {
			name string
			fn   func() error
		}

		// 应用层清理步骤可并行执行，基础设施资源（Redis/Ent）最后按顺序关闭。
		parallelSteps := []cleanupStep{
			{"OpsScheduledReportService", func() error {
				if opsScheduledReport != nil {
					opsScheduledReport.Stop()
				}
				return nil
			}},
			{"OpsCleanupService", func() error {
				if opsCleanup != nil {
					opsCleanup.Stop()
				}
				return nil
			}},
			{"OpsSystemLogSink", func() error {
				if opsSystemLogSink != nil {
					opsSystemLogSink.Stop()
				}
				return nil
			}},
			{"OpsAlertEvaluatorService", func() error {
				if opsAlertEvaluator != nil {
					opsAlertEvaluator.Stop()
				}
				return nil
			}},
			{"OpsAggregationService", func() error {
				if opsAggregation != nil {
					opsAggregation.Stop()
				}
				return nil
			}},
			{"OpsMetricsCollector", func() error {
				if opsMetricsCollector != nil {
					opsMetricsCollector.Stop()
				}
				return nil
			}},
			{"SchedulerSnapshotService", func() error {
				if schedulerSnapshot != nil {
					schedulerSnapshot.Stop()
				}
				return nil
			}},
			// TK fix for upstream Wei-Shaw/sub2api#2538 — see
			// internal/service/scheduler_rate_limit_reaper.go.
			{"SchedulerRateLimitReaper", func() error {
				if schedulerRateLimitReaper != nil {
					schedulerRateLimitReaper.Stop()
				}
				return nil
			}},
			{"AnthropicConfigReconciler", func() error {
				if anthropicConfigReconciler != nil {
					anthropicConfigReconciler.Stop()
				}
				return nil
			}},
			{"AntigravityConfigReconciler", func() error {
				if antigravityConfigReconciler != nil {
					antigravityConfigReconciler.Stop()
				}
				return nil
			}},
			{"UpstreamBalanceSentinel", func() error {
				if upstreamBalanceSentinel != nil {
					upstreamBalanceSentinel.Stop()
				}
				return nil
			}},
			{"UsageCleanupService", func() error {
				if usageCleanup != nil {
					usageCleanup.Stop()
				}
				return nil
			}},
			{"IdempotencyCleanupService", func() error {
				if idempotencyCleanup != nil {
					idempotencyCleanup.Stop()
				}
				return nil
			}},
			{"HoldReconcilerService", func() error {
				if holdReconciler != nil {
					holdReconciler.Stop()
				}
				return nil
			}},
			{"TokenRefreshService", func() error {
				tokenRefresh.Stop()
				return nil
			}},
			{"AccountExpiryService", func() error {
				accountExpiry.Stop()
				return nil
			}},
			{"ProxyExpiryService", func() error {
				proxyExpiry.Stop()
				return nil
			}},
			{"SubscriptionExpiryService", func() error {
				subscriptionExpiry.Stop()
				return nil
			}},
			{"SubscriptionService", func() error {
				if subscriptionService != nil {
					subscriptionService.Stop()
				}
				return nil
			}},
			{"PricingService", func() error {
				pricing.Stop()
				return nil
			}},
			{"EmailQueueService", func() error {
				emailQueue.Stop()
				return nil
			}},
			{"BillingCacheService", func() error {
				billingCache.Stop()
				return nil
			}},
			{"UsageRecordWorkerPool", func() error {
				if usageRecordWorkerPool != nil {
					usageRecordWorkerPool.Stop()
				}
				return nil
			}},
			{"QACaptureService", func() error {
				if qaCapture != nil {
					qaCapture.Stop()
				}
				return nil
			}},
			{"OAuthService", func() error {
				oauth.Stop()
				return nil
			}},
			{"OpenAIOAuthService", func() error {
				openaiOAuth.Stop()
				return nil
			}},
			{"GeminiOAuthService", func() error {
				geminiOAuth.Stop()
				return nil
			}},
			{"AntigravityOAuthService", func() error {
				antigravityOAuth.Stop()
				return nil
			}},
			{"GrokOAuthService", func() error {
				if grokOAuth != nil {
					grokOAuth.Stop()
				}
				return nil
			}},
			{"OpenAIWSPool", func() error {
				if openAIGateway != nil {
					openAIGateway.CloseOpenAIWSPool()
				}
				return nil
			}},
			{"ScheduledTestRunnerService", func() error {
				if scheduledTestRunner != nil {
					scheduledTestRunner.Stop()
				}
				return nil
			}},
			{"BackupService", func() error {
				if backupSvc != nil {
					backupSvc.Stop()
				}
				return nil
			}},
			{"PaymentOrderExpiryService", func() error {
				if paymentOrderExpiry != nil {
					paymentOrderExpiry.Stop()
				}
				return nil
			}},
			{"ChannelMonitorRunner", func() error {
				if channelMonitorRunner != nil {
					channelMonitorRunner.Stop()
				}
				return nil
			}},
			{"AccountIncidentNotifier", func() error {
				if accountIncidentNotifier != nil {
					accountIncidentNotifier.Stop()
				}
				return nil
			}},
			{"PricingMissingNotifier", func() error {
				if pricingMissingNotifier != nil {
					pricingMissingNotifier.Stop()
				}
				return nil
			}},
		}

		infraSteps := []cleanupStep{
			{"Redis", func() error {
				if rdb == nil {
					return nil
				}
				return rdb.Close()
			}},
			{"Ent", func() error {
				if entClient == nil {
					return nil
				}
				return entClient.Close()
			}},
		}

		runParallel := func(steps []cleanupStep) {
			var wg sync.WaitGroup
			for i := range steps {
				step := steps[i]
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := step.fn(); err != nil {
						log.Printf("[Cleanup] %s failed: %v", step.name, err)
						return
					}
					log.Printf("[Cleanup] %s succeeded", step.name)
				}()
			}
			wg.Wait()
		}

		runSequential := func(steps []cleanupStep) {
			for i := range steps {
				step := steps[i]
				if err := step.fn(); err != nil {
					log.Printf("[Cleanup] %s failed: %v", step.name, err)
					continue
				}
				log.Printf("[Cleanup] %s succeeded", step.name)
			}
		}

		runParallel(parallelSteps)
		runSequential(infraSteps)

		// Check if context timed out
		select {
		case <-ctx.Done():
			log.Printf("[Cleanup] Warning: cleanup timed out after 10 seconds")
		default:
			log.Printf("[Cleanup] All cleanup steps completed")
		}
	}
}
