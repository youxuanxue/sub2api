package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	qaobs "github.com/Wei-Shaw/sub2api/internal/observability/qa"
)

// AdminHandlers contains all admin-related HTTP handlers
type AdminHandlers struct {
	Dashboard              *admin.DashboardHandler
	User                   *admin.UserHandler
	Group                  *admin.GroupHandler
	Account                *admin.AccountHandler
	Announcement           *admin.AnnouncementHandler
	DataManagement         *admin.DataManagementHandler
	Backup                 *admin.BackupHandler
	OAuth                  *admin.OAuthHandler
	OpenAIOAuth            *admin.OpenAIOAuthHandler
	GeminiOAuth            *admin.GeminiOAuthHandler
	AntigravityOAuth       *admin.AntigravityOAuthHandler
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
	APIKey                 *admin.AdminAPIKeyHandler
	ScheduledTest          *admin.ScheduledTestHandler
	Channel                *admin.ChannelHandler
	ChannelMonitor         *admin.ChannelMonitorHandler
	ChannelMonitorTemplate *admin.ChannelMonitorRequestTemplateHandler
	ContentModeration      *admin.ContentModerationHandler
	Payment                *admin.PaymentHandler
	Affiliate              *admin.AffiliateHandler
	Compliance             *admin.ComplianceHandler
	TKChannel              *admin.TKChannelAdminHandler
	// TK: anthropic-oauth stability tier reference table CRUD — see tier_handler_tk.go.
	Tier *admin.TierHandler
	// TK: prod-side cross-edge read-only account overview — see edge_accounts_handler_tk.go.
	EdgeAccounts *admin.EdgeAccountsHandler
	// TK: prod-side thin proxy for inline edge-account WRITE ops (forwards to each
	// edge's least-privilege ops endpoint) — see edge_account_ops_handler_tk.go.
	EdgeAccountOps *admin.EdgeAccountOpsHandler
	// TK: Invite-to-Trial batch provisioning + 试用方案 presets — see user_handler_tk_provision.go.
	TrialProvision *admin.TrialProvisionHandler
}

// Handlers contains all HTTP handlers
type Handlers struct {
	Auth             *AuthHandler
	User             *UserHandler
	APIKey           *APIKeyHandler
	Usage            *UsageHandler
	Redeem           *RedeemHandler
	Subscription     *SubscriptionHandler
	Announcement     *AnnouncementHandler
	ChannelMonitor   *ChannelMonitorUserHandler
	Admin            *AdminHandlers
	Gateway          *GatewayHandler
	OpenAIGateway    *OpenAIGatewayHandler
	Setting          *SettingHandler
	Totp             *TotpHandler
	Payment          *PaymentHandler
	PaymentWebhook   *PaymentWebhookHandler
	AvailableChannel *AvailableChannelHandler
	QACapture        *qaobs.Service
	// TK: public model + pricing catalog (US-028 / docs/approved/user-cold-start.md §2 v1).
	PricingCatalog *PricingCatalogHandler
	// TK: per-user "your menu" view backing GET /api/v1/me/pricing-catalog.
	MePricingCatalog *MePricingCatalogHandler
	// TK: user-facing QA export (issue #59 / docs/approved/ops-unified-contract.md §2).
	QA *QAHandler
	// TK: internal edge capacity read (surface C) — see edge_tk_capacity_handler.go.
	EdgeCapacity *EdgeCapacityHandler
	// TK: internal edge read-only account inventory — see edge_tk_accounts_handler.go.
	EdgeAccounts *EdgeAccountsHandler
	// TK: edge admin-session mint for the prod→edge "manage accounts" handoff —
	// see edge_tk_admin_session_handler.go.
	EdgeAdminSession *EdgeAdminSessionHandler
	// TK: edge least-privilege account WRITE ops (clear-rate-limit / reset-quota /
	// temp-unschedulable / schedulable / usage) the prod /accounts page proxies to
	// for inline edge-account management — see edge_tk_account_ops_handler.go.
	EdgeAccountOps *EdgeAccountOpsHandler
}

// BuildInfo contains build-time information
type BuildInfo struct {
	Version   string
	BuildType string // "source" for manual builds, "release" for CI builds
}
