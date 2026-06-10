package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	qaobs "github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
)

// ProvideAdminHandlers creates the AdminHandlers struct
func ProvideAdminHandlers(
	dashboardHandler *admin.DashboardHandler,
	userHandler *admin.UserHandler,
	groupHandler *admin.GroupHandler,
	accountHandler *admin.AccountHandler,
	announcementHandler *admin.AnnouncementHandler,
	dataManagementHandler *admin.DataManagementHandler,
	backupHandler *admin.BackupHandler,
	oauthHandler *admin.OAuthHandler,
	openaiOAuthHandler *admin.OpenAIOAuthHandler,
	geminiOAuthHandler *admin.GeminiOAuthHandler,
	antigravityOAuthHandler *admin.AntigravityOAuthHandler,
	proxyHandler *admin.ProxyHandler,
	redeemHandler *admin.RedeemHandler,
	promoHandler *admin.PromoHandler,
	settingHandler *admin.SettingHandler,
	opsHandler *admin.OpsHandler,
	systemHandler *admin.SystemHandler,
	subscriptionHandler *admin.SubscriptionHandler,
	usageHandler *admin.UsageHandler,
	userAttributeHandler *admin.UserAttributeHandler,
	errorPassthroughHandler *admin.ErrorPassthroughHandler,
	tlsFingerprintProfileHandler *admin.TLSFingerprintProfileHandler,
	apiKeyHandler *admin.AdminAPIKeyHandler,
	scheduledTestHandler *admin.ScheduledTestHandler,
	channelHandler *admin.ChannelHandler,
	channelMonitorHandler *admin.ChannelMonitorHandler,
	channelMonitorTemplateHandler *admin.ChannelMonitorRequestTemplateHandler,
	contentModerationHandler *admin.ContentModerationHandler,
	paymentHandler *admin.PaymentHandler,
	affiliateHandler *admin.AffiliateHandler,
	complianceHandler *admin.ComplianceHandler,
	tkChannelHandler *admin.TKChannelAdminHandler,
	tierHandler *admin.TierHandler,
	edgeAccountsHandler *admin.EdgeAccountsHandler,
) *AdminHandlers {
	return &AdminHandlers{
		Dashboard:              dashboardHandler,
		User:                   userHandler,
		Group:                  groupHandler,
		Account:                accountHandler,
		Announcement:           announcementHandler,
		DataManagement:         dataManagementHandler,
		Backup:                 backupHandler,
		OAuth:                  oauthHandler,
		OpenAIOAuth:            openaiOAuthHandler,
		GeminiOAuth:            geminiOAuthHandler,
		AntigravityOAuth:       antigravityOAuthHandler,
		Proxy:                  proxyHandler,
		Redeem:                 redeemHandler,
		Promo:                  promoHandler,
		Setting:                settingHandler,
		Ops:                    opsHandler,
		System:                 systemHandler,
		Subscription:           subscriptionHandler,
		Usage:                  usageHandler,
		UserAttribute:          userAttributeHandler,
		ErrorPassthrough:       errorPassthroughHandler,
		TLSFingerprintProfile:  tlsFingerprintProfileHandler,
		APIKey:                 apiKeyHandler,
		ScheduledTest:          scheduledTestHandler,
		Channel:                channelHandler,
		ChannelMonitor:         channelMonitorHandler,
		ChannelMonitorTemplate: channelMonitorTemplateHandler,
		ContentModeration:      contentModerationHandler,
		Payment:                paymentHandler,
		Affiliate:              affiliateHandler,
		Compliance:             complianceHandler,
		TKChannel:              tkChannelHandler,
		Tier:                   tierHandler,
		EdgeAccounts:           edgeAccountsHandler,
	}
}

// ProvideSystemHandler creates admin.SystemHandler with UpdateService
func ProvideSystemHandler(updateService *service.UpdateService, lockService *service.SystemOperationLockService) *admin.SystemHandler {
	return admin.NewSystemHandler(updateService, lockService)
}

// ProvideSettingHandler creates SettingHandler with version from BuildInfo
func ProvideSettingHandler(settingService *service.SettingService, buildInfo BuildInfo, notificationEmailService *service.NotificationEmailService) *SettingHandler {
	h := NewSettingHandler(settingService, buildInfo.Version)
	h.SetNotificationEmailService(notificationEmailService)
	return h
}

// ProvideAdminSettingHandler creates admin.SettingHandler with notification template APIs.
func ProvideAdminSettingHandler(settingService *service.SettingService, emailService *service.EmailService, turnstileService *service.TurnstileService, opsService *service.OpsService, paymentConfigService *service.PaymentConfigService, paymentService *service.PaymentService, userAttributeService *service.UserAttributeService, notificationEmailService *service.NotificationEmailService) *admin.SettingHandler {
	h := admin.NewSettingHandler(settingService, emailService, turnstileService, opsService, paymentConfigService, paymentService, userAttributeService)
	h.SetNotificationEmailService(notificationEmailService)
	return h
}

// TKGatewayHandlerModelListReady is a wire sentinel: holding it proves
// GatewayHandler.SetModelListFilter has been called with the production
// ModelListFilter. provideCleanup (cmd/server/wire.go) takes this type as an
// unused parameter to force wire to evaluate the side-effect.
type TKGatewayHandlerModelListReady struct{}

// ProvideTKGatewayHandlerModelList wires the model-list filter onto
// GatewayHandler post-construction. Mirrors ProvideTKGatewayPricingAvailability
// in shape; SetModelListFilter is nil-safe (degraded → fail-open).
func ProvideTKGatewayHandlerModelList(
	h *GatewayHandler,
	f *service.ModelListFilter,
) TKGatewayHandlerModelListReady {
	if h != nil {
		h.SetModelListFilter(f)
	}
	return TKGatewayHandlerModelListReady{}
}

// ProvideTKPricingCatalogHandler wraps the upstream-shape NewPricingCatalogHandler
// constructor with TK-only post-construction wiring for the pricing-availability
// observability service. Keeping NewPricingCatalogHandler's signature stable
// (CLAUDE.md §5 — minimal injection point) lets upstream merges of the
// constructor not touch TK extensions, AND the assignment survives `go run wire`
// regenerations (a manual edit in wire_gen.go would not).
//
// Mirrors ProvideOpenAIGatewayHandler in shape.
func ProvideTKPricingCatalogHandler(
	catalog *service.PricingCatalogService,
	gate *service.SettingService,
	avail *service.PricingAvailabilityService,
) *PricingCatalogHandler {
	h := NewPricingCatalogHandler(catalog, gate)
	h.SetAvailabilityService(avail)
	return h
}

// ProvideOpenAIGatewayHandler wraps the upstream-shape NewOpenAIGatewayHandler
// constructor with TK-only post-construction wiring. Keeping the signature of
// NewOpenAIGatewayHandler stable (CLAUDE.md §5 — minimal injection point) and
// doing post-wiring here means upstream merges of the constructor never touch
// TK extensions, AND the assignment survives `go run wire` regenerations
// (the manual edit anti-pattern in wire_gen.go would not).
//
// Mirrors the existing `ProvideRateLimitService` shape in service/wire.go.
func ProvideOpenAIGatewayHandler(
	gatewayService *service.OpenAIGatewayService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	apiKeyService *service.APIKeyService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	errorPassthroughService *service.ErrorPassthroughService,
	contentModerationService *service.ContentModerationService,
	cfg *config.Config,
	videoTaskCache service.VideoTaskCache,
) *OpenAIGatewayHandler {
	h := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingCacheService,
		apiKeyService,
		usageRecordWorkerPool,
		errorPassthroughService,
		contentModerationService,
		cfg,
	)
	h.SetVideoTaskCache(videoTaskCache)
	return h
}

// ProvideEdgeCapacityHandler adapts the wire-provided service.AccountRepository
// (which satisfies the handler's narrow schedulingCapacityReader interface) to
// the edge capacity handler. A dedicated provider avoids needing a wire.Bind for
// the unexported interface and keeps NewEdgeCapacityHandler unit-test friendly.
func ProvideEdgeCapacityHandler(accountRepo service.AccountRepository) *EdgeCapacityHandler {
	return NewEdgeCapacityHandler(accountRepo)
}

// ProvideEdgeAccountsHandler adapts the wire-provided account repository plus the
// live-gauge services (concurrency / session-limit / rpm / usage — the same set
// admin AccountHandler uses) to the edge accounts read handler. The gauge readers
// let the edge endpoint surface per-edge capacity/today figures that align with
// the per-edge admin accounts page. Mirrors ProvideEdgeCapacityHandler in shape.
func ProvideEdgeAccountsHandler(
	adminService service.AdminService,
	concurrencyService *service.ConcurrencyService,
	sessionLimitCache service.SessionLimitCache,
	rpmCache service.RPMCache,
	accountUsageService *service.AccountUsageService,
) *EdgeAccountsHandler {
	return NewEdgeAccountsHandler(adminService, concurrencyService, sessionLimitCache, rpmCache, accountUsageService)
}

// ProvideEdgeAdminSessionHandler adapts the wire-provided concrete services
// (which satisfy the handler's narrow lookup/minter interfaces) to the edge
// admin-session mint handler. A dedicated provider avoids wire.Bind for the
// unexported interfaces; mirrors ProvideEdgeAccountsHandler in shape.
func ProvideEdgeAdminSessionHandler(
	apiKeyService *service.APIKeyService,
	userService *service.UserService,
	authService *service.AuthService,
) *EdgeAdminSessionHandler {
	return NewEdgeAdminSessionHandler(apiKeyService, userService, authService)
}

// ProvideTKEdgeAccountsAdminHandler adapts the wire-provided concrete
// *service.EdgeAccountsAggregator (which satisfies the admin handler's narrow
// interface) to the prod-side cross-edge account overview handler. A dedicated
// provider avoids a wire.Bind for the unexported interface.
func ProvideTKEdgeAccountsAdminHandler(agg *service.EdgeAccountsAggregator) *admin.EdgeAccountsHandler {
	return admin.NewEdgeAccountsHandler(agg)
}

// ProvideHandlers creates the Handlers struct
func ProvideHandlers(
	authHandler *AuthHandler,
	userHandler *UserHandler,
	apiKeyHandler *APIKeyHandler,
	usageHandler *UsageHandler,
	redeemHandler *RedeemHandler,
	subscriptionHandler *SubscriptionHandler,
	announcementHandler *AnnouncementHandler,
	channelMonitorUserHandler *ChannelMonitorUserHandler,
	adminHandlers *AdminHandlers,
	gatewayHandler *GatewayHandler,
	openaiGatewayHandler *OpenAIGatewayHandler,
	settingHandler *SettingHandler,
	totpHandler *TotpHandler,
	paymentHandler *PaymentHandler,
	paymentWebhookHandler *PaymentWebhookHandler,
	availableChannelHandler *AvailableChannelHandler,
	qaService *qaobs.Service,
	pricingCatalogHandler *PricingCatalogHandler,
	mePricingCatalogHandler *MePricingCatalogHandler,
	qaHandler *QAHandler,
	edgeCapacityHandler *EdgeCapacityHandler,
	edgeAccountsHandler *EdgeAccountsHandler,
	edgeAdminSessionHandler *EdgeAdminSessionHandler,
	_ *service.IdempotencyCoordinator,
	_ *service.IdempotencyCleanupService,
) *Handlers {
	return &Handlers{
		Auth:             authHandler,
		User:             userHandler,
		APIKey:           apiKeyHandler,
		Usage:            usageHandler,
		Redeem:           redeemHandler,
		Subscription:     subscriptionHandler,
		Announcement:     announcementHandler,
		ChannelMonitor:   channelMonitorUserHandler,
		Admin:            adminHandlers,
		Gateway:          gatewayHandler,
		OpenAIGateway:    openaiGatewayHandler,
		Setting:          settingHandler,
		Totp:             totpHandler,
		Payment:          paymentHandler,
		PaymentWebhook:   paymentWebhookHandler,
		AvailableChannel: availableChannelHandler,
		QACapture:        qaService,
		PricingCatalog:   pricingCatalogHandler,
		MePricingCatalog: mePricingCatalogHandler,
		QA:               qaHandler,
		EdgeCapacity:     edgeCapacityHandler,
		EdgeAccounts:     edgeAccountsHandler,
		EdgeAdminSession: edgeAdminSessionHandler,
	}
}

// ProviderSet is the Wire provider set for all handlers
var ProviderSet = wire.NewSet(
	// Top-level handlers
	NewAuthHandler,
	NewUserHandler,
	NewAPIKeyHandler,
	NewUsageHandler,
	NewRedeemHandler,
	NewSubscriptionHandler,
	NewAnnouncementHandler,
	NewChannelMonitorUserHandler,
	NewGatewayHandler,
	ProvideOpenAIGatewayHandler,
	NewTotpHandler,
	ProvideSettingHandler,
	NewPaymentHandler,
	NewPaymentWebhookHandler,
	NewAvailableChannelHandler,
	// TK: pricing-availability observability — see docs/approved/pricing-availability-source-of-truth.md
	ProvideTKPricingCatalogHandler,
	// TK: per-user pricing catalog ("Your Menu") — see me_pricing_catalog_handler_tk.go
	NewMePricingCatalogHandler,
	ProvideTKGatewayHandlerModelList,
	NewQAHandler,
	// TK: internal edge capacity read (surface C) — see edge_tk_capacity_handler.go.
	ProvideEdgeCapacityHandler,
	// TK: internal edge read-only account inventory — see edge_tk_accounts_handler.go.
	ProvideEdgeAccountsHandler,
	// TK: edge admin-session mint for prod→edge "manage accounts" handoff — see edge_tk_admin_session_handler.go.
	ProvideEdgeAdminSessionHandler,

	// Admin handlers
	admin.NewDashboardHandler,
	admin.NewUserHandler,
	admin.NewGroupHandler,
	admin.NewAccountHandler,
	admin.NewAnnouncementHandler,
	admin.NewDataManagementHandler,
	admin.NewBackupHandler,
	admin.NewOAuthHandler,
	admin.NewOpenAIOAuthHandler,
	admin.NewGeminiOAuthHandler,
	admin.NewAntigravityOAuthHandler,
	admin.NewProxyHandler,
	admin.NewRedeemHandler,
	admin.NewPromoHandler,
	ProvideAdminSettingHandler,
	admin.NewOpsHandler,
	ProvideSystemHandler,
	admin.NewSubscriptionHandler,
	admin.NewUsageHandler,
	admin.NewUserAttributeHandler,
	admin.NewErrorPassthroughHandler,
	admin.NewTLSFingerprintProfileHandler,
	admin.NewAdminAPIKeyHandler,
	admin.NewScheduledTestHandler,
	admin.NewChannelHandler,
	admin.NewChannelMonitorHandler,
	admin.NewChannelMonitorRequestTemplateHandler,
	admin.NewContentModerationHandler,
	admin.NewTKChannelAdminHandler,
	admin.NewPaymentHandler,
	admin.NewAffiliateHandler,
	admin.NewComplianceHandler,
	// TK: anthropic-oauth stability tier reference table CRUD — see tier_handler_tk.go.
	admin.NewTierHandler,
	// TK: prod-side cross-edge read-only account overview — see edge_accounts_handler_tk.go.
	ProvideTKEdgeAccountsAdminHandler,

	// AdminHandlers and Handlers constructors
	ProvideAdminHandlers,
	ProvideHandlers,
)
