package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	qaobs "github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
)

// AdminHandlers contains all admin-related HTTP handlers
type AdminHandlers struct {
	Dashboard              *admin.DashboardHandler
	User                   *admin.UserHandler
	Group                  *admin.GroupHandler
	Account                *admin.AccountHandler
	SupplierSource         *admin.SupplierSourceHandler
	Announcement           *admin.AnnouncementHandler
	DataManagement         *admin.DataManagementHandler
	Backup                 *admin.BackupHandler
	OAuth                  *admin.OAuthHandler
	OpenAIOAuth            *admin.OpenAIOAuthHandler
	GeminiOAuth            *admin.GeminiOAuthHandler
	AntigravityOAuth       *admin.AntigravityOAuthHandler
	GrokOAuth              *admin.GrokOAuthHandler
	CNProvider             *admin.CNProviderHandler
	Proxy                  *admin.ProxyHandler
	Redeem                 *admin.RedeemHandler
	Promo                  *admin.PromoHandler
	Setting                *admin.SettingHandler
	Ops                    *admin.OpsHandler
	System                 *admin.SystemHandler
	Subscription           *admin.SubscriptionHandler
	Usage                  *admin.UsageHandler
	UserAttribute          *admin.UserAttributeHandler
	ErrorPassthrough       *admin.ErrorPassthroughHandler
	TLSFingerprintProfile  *admin.TLSFingerprintProfileHandler
	Plugin                 *admin.PluginHandler
	APIKey                 *admin.AdminAPIKeyHandler
	ScheduledTest          *admin.ScheduledTestHandler
	Channel                *admin.ChannelHandler
	ChannelMonitor         *admin.ChannelMonitorHandler
	ChannelMonitorTemplate *admin.ChannelMonitorRequestTemplateHandler
	ContentModeration      *admin.ContentModerationHandler
	PromptAudit            *securityaudit.PromptAdminHandler
	Payment                *admin.PaymentHandler
	Affiliate              *admin.AffiliateHandler
	Compliance             *admin.ComplianceHandler
	TKChannel              *admin.TKChannelAdminHandler
	// TK: anthropic-oauth stability tier reference table CRUD — see tier_handler_tk.go.
	Tier *admin.TierHandler
	// TK: Invite-to-Trial batch provisioning + 试用方案 presets — see user_handler_tk_provision.go.
	TrialProvision *admin.TrialProvisionHandler
	AuditLog       *admin.AuditLogHandler
}

// Handlers contains all HTTP handlers
type Handlers struct {
	Auth             *AuthHandler
	User             *UserHandler
	APIKey           *APIKeyHandler
	Feedback         *FeedbackHandler
	Usage            *UsageHandler
	Redeem           *RedeemHandler
	Subscription     *SubscriptionHandler
	Announcement     *AnnouncementHandler
	ChannelMonitor   *ChannelMonitorUserHandler
	ChannelMonitorV2 *ChannelMonitorV2Handler
	Admin            *AdminHandlers
	Gateway          *GatewayHandler
	OpenAIGateway    *OpenAIGatewayHandler
	Setting          *SettingHandler
	Totp             *TotpHandler
	Passkey          *PasskeyHandler
	Payment          *PaymentHandler
	PaymentWebhook   *PaymentWebhookHandler
	AvailableChannel *AvailableChannelHandler
	ModelPlaza       *ModelPlazaHandler
	QACapture        *qaobs.Service
	// TK: public model + pricing catalog (US-028 / docs/approved/user-cold-start.md §2 v1).
	PricingCatalog *PricingCatalogHandler
	// TK: per-user "your menu" view backing GET /api/v1/me/pricing-catalog.
	MePricingCatalog *MePricingCatalogHandler
	GeminiWebSession *GeminiWebSessionHandler
	AsyncImage       *AsyncImageHandler
	BatchImage       *BatchImageHandler
}

// BuildInfo contains build-time information
type BuildInfo struct {
	Version   string
	BuildType string // "source" for manual builds, "release" for CI builds
}
