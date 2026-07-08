/**
 * Shared settings state composable for SettingsView tab panels.
 *
 * This composable holds the form reactive object and operations (load, save)
 * that are shared across all settings tab panels.  Each panel component
 * calls `useSettingsState()` to obtain the same singleton instance
 * provided by the root SettingsView via Vue's provide/inject.
 */

import { inject, provide, type InjectionKey } from "vue";
import type { Ref, Reactive, ComputedRef } from "vue";
import type {
  AuthSourceDefaultsState,
  AuthSourceType,
  DefaultPlatformQuotasMap,
} from "@/api/admin/settings";
import type {
  AdminGroup,
  LoginAgreementDocument,
  NotifyEmailEntry,
} from "@/types";

// ── Re-export the SettingsTab union so panels can reference it ──
export type SettingsTab =
  | "general"
  | "agreement"
  | "features"
  | "security"
  | "users"
  | "gateway"
  | "payment"
  | "email"
  | "backup";

// ── SettingsForm type (mirrors the form reactive in SettingsView) ──
// We keep it here so both the root view and panels can import it.

export interface CustomMenuItem {
  id: string;
  label: string;
  icon_svg: string;
  url: string;
  visibility: "user" | "admin";
  sort_order: number;
}

export interface CustomEndpoint {
  name: string;
  endpoint: string;
  description: string;
}

export type SettingsForm = {
  registration_enabled: boolean;
  email_verify_enabled: boolean;
  registration_email_suffix_whitelist: string[];
  promo_code_enabled: boolean;
  invitation_code_enabled: boolean;
  password_reset_enabled: boolean;
  totp_enabled: boolean;
  totp_encryption_key_configured: boolean;
  login_agreement_enabled: boolean;
  login_agreement_mode: string;
  login_agreement_updated_at: string;
  login_agreement_documents: LoginAgreementDocument[];
  default_balance: number;
  default_platform_quotas: DefaultPlatformQuotasMap;
  affiliate_rebate_rate: number;
  affiliate_rebate_freeze_hours: number;
  affiliate_rebate_duration_days: number;
  affiliate_rebate_per_invitee_cap: number;
  default_concurrency: number;
  default_subscriptions: Array<{ group_id: number; validity_days: number }>;
  site_name: string;
  site_logo: string;
  site_subtitle: string;
  api_base_url: string;
  contact_info: string;
  doc_url: string;
  home_content: string;
  force_email_on_third_party_signup: boolean;
  default_user_rpm_limit: number;
  backend_mode_enabled: boolean;
  hide_ccs_import_button: boolean;
  payment_enabled: boolean;
  risk_control_enabled: boolean;
  cyber_session_block_enabled: boolean;
  cyber_session_block_ttl_seconds: number;
  payment_min_amount: number;
  payment_max_amount: number;
  payment_daily_limit: number;
  payment_max_pending_orders: number;
  payment_order_timeout_minutes: number;
  payment_balance_disabled: boolean;
  payment_balance_recharge_multiplier: number;
  payment_subscription_usd_to_cny_rate: number;
  payment_recharge_fee_rate: number;
  payment_enabled_types: string[];
  payment_help_image_url: string;
  payment_help_text: string;
  payment_product_name_prefix: string;
  payment_product_name_suffix: string;
  payment_load_balance_strategy: string;
  payment_cancel_rate_limit_enabled: boolean;
  payment_cancel_rate_limit_max: number;
  payment_cancel_rate_limit_window: number;
  payment_cancel_rate_limit_unit: string;
  payment_cancel_rate_limit_window_mode: string;
  payment_alipay_force_qrcode: boolean;
  table_default_page_size: number;
  table_page_size_options: number[];
  custom_menu_items: CustomMenuItem[];
  custom_endpoints: CustomEndpoint[];
  frontend_url: string;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  smtp_password: string;
  smtp_password_configured: boolean;
  smtp_from_email: string;
  smtp_from_name: string;
  smtp_use_tls: boolean;
  turnstile_enabled: boolean;
  turnstile_site_key: string;
  turnstile_secret_key: string;
  turnstile_secret_key_configured: boolean;
  api_key_acl_trust_forwarded_ip: boolean;
  linuxdo_connect_enabled: boolean;
  linuxdo_connect_client_id: string;
  linuxdo_connect_client_secret: string;
  linuxdo_connect_client_secret_configured: boolean;
  linuxdo_connect_redirect_url: string;
  dingtalk_connect_enabled: boolean;
  dingtalk_connect_client_id: string;
  dingtalk_connect_client_secret: string;
  dingtalk_connect_client_secret_configured: boolean;
  dingtalk_connect_redirect_url: string;
  dingtalk_connect_corp_restriction_policy: string;
  dingtalk_connect_internal_corp_id: string;
  dingtalk_connect_bypass_registration: boolean;
  dingtalk_connect_sync_corp_email: boolean;
  dingtalk_connect_sync_display_name: boolean;
  dingtalk_connect_sync_dept: boolean;
  dingtalk_connect_sync_corp_email_attr_key: string;
  dingtalk_connect_sync_display_name_attr_key: string;
  dingtalk_connect_sync_dept_attr_key: string;
  dingtalk_connect_sync_corp_email_attr_name: string;
  dingtalk_connect_sync_display_name_attr_name: string;
  dingtalk_connect_sync_dept_attr_name: string;
  wechat_connect_enabled: boolean;
  wechat_connect_app_id: string;
  wechat_connect_app_secret: string;
  wechat_connect_app_secret_configured: boolean;
  wechat_connect_open_app_id: string;
  wechat_connect_open_app_secret: string;
  wechat_connect_open_app_secret_configured: boolean;
  wechat_connect_mp_app_id: string;
  wechat_connect_mp_app_secret: string;
  wechat_connect_mp_app_secret_configured: boolean;
  wechat_connect_mobile_app_id: string;
  wechat_connect_mobile_app_secret: string;
  wechat_connect_mobile_app_secret_configured: boolean;
  wechat_connect_open_enabled: boolean;
  wechat_connect_mp_enabled: boolean;
  wechat_connect_mobile_enabled: boolean;
  wechat_connect_mode: string;
  wechat_connect_scopes: string;
  wechat_connect_redirect_url: string;
  wechat_connect_frontend_redirect_url: string;
  oidc_connect_enabled: boolean;
  oidc_connect_provider_name: string;
  oidc_connect_client_id: string;
  oidc_connect_client_secret: string;
  oidc_connect_client_secret_configured: boolean;
  oidc_connect_issuer_url: string;
  oidc_connect_discovery_url: string;
  oidc_connect_authorize_url: string;
  oidc_connect_token_url: string;
  oidc_connect_userinfo_url: string;
  oidc_connect_jwks_url: string;
  oidc_connect_scopes: string;
  oidc_connect_redirect_url: string;
  oidc_connect_frontend_redirect_url: string;
  oidc_connect_token_auth_method: string;
  oidc_connect_use_pkce: boolean;
  oidc_connect_validate_id_token: boolean;
  oidc_connect_allowed_signing_algs: string;
  oidc_connect_clock_skew_seconds: number;
  oidc_connect_require_email_verified: boolean;
  oidc_connect_userinfo_email_path: string;
  oidc_connect_userinfo_id_path: string;
  oidc_connect_userinfo_username_path: string;
  github_oauth_enabled: boolean;
  github_oauth_client_id: string;
  github_oauth_client_secret: string;
  github_oauth_client_secret_configured: boolean;
  github_oauth_redirect_url: string;
  github_oauth_frontend_redirect_url: string;
  google_oauth_enabled: boolean;
  google_oauth_client_id: string;
  google_oauth_client_secret: string;
  google_oauth_client_secret_configured: boolean;
  google_oauth_redirect_url: string;
  google_oauth_frontend_redirect_url: string;
  enable_model_fallback: boolean;
  fallback_model_anthropic: string;
  fallback_model_openai: string;
  fallback_model_gemini: string;
  fallback_model_antigravity: string;
  enable_identity_patch: boolean;
  identity_patch_prompt: string;
  ops_monitoring_enabled: boolean;
  ops_realtime_monitoring_enabled: boolean;
  ops_query_mode_default: string;
  ops_metrics_interval_seconds: number;
  min_claude_code_version: string;
  max_claude_code_version: string;
  allow_ungrouped_key_scheduling: boolean;
  openai_advanced_scheduler_enabled: boolean;
  enable_fingerprint_unification: boolean;
  enable_metadata_passthrough: boolean;
  enable_cch_signing: boolean;
  enable_claude_oauth_system_prompt_injection: boolean;
  claude_oauth_system_prompt: string;
  claude_oauth_system_prompt_blocks: string;
  enable_anthropic_cache_ttl_1h_injection: boolean;
  tk_anthropic_request_normalize_enabled: boolean;
  sticky_routing_enabled: boolean;
  rewrite_message_cache_control: boolean;
  enable_client_dateline_normalization: boolean;
  antigravity_user_agent_version: string;
  openai_codex_user_agent: string;
  min_codex_version: string;
  max_codex_version: string;
  codex_cli_only_blacklist: string;
  codex_cli_only_whitelist: string;
  codex_cli_only_allow_app_server_clients: boolean;
  codex_cli_only_engine_fingerprint_signals: string;
  balance_low_notify_enabled: boolean;
  balance_low_notify_threshold: number;
  balance_low_notify_recharge_url: string;
  subscription_expiry_notify_enabled: boolean;
  account_quota_notify_enabled: boolean;
  account_quota_notify_emails: NotifyEmailEntry[];
  signup_bonus_enabled: boolean;
  signup_bonus_balance: number;
  auto_generate_default_token: boolean;
  auto_generate_default_token_name: string;
  pricing_catalog_public: boolean;
  channel_monitor_enabled: boolean;
  channel_monitor_default_interval_seconds: number;
  available_channels_enabled: boolean;
  affiliate_enabled: boolean;
  allow_user_view_error_requests: boolean;
};

// ── Injection key ──

export interface SettingsStateContext {
  form: Reactive<SettingsForm>;
  saving: Ref<boolean>;
  loading: Ref<boolean>;
  loadFailed: Ref<boolean>;
  activeTab: Ref<SettingsTab>;

  // Shared helpers
  localText: (zh: string, en: string) => string;
  isZhLocale: ComputedRef<boolean>;
  currentOrigin: string;
  buildApiCallbackUrl: (path: string) => string;

  // Subscription groups (used by Users and Security tabs)
  subscriptionGroups: Ref<AdminGroup[]>;
  defaultSubscriptionGroupOptions: ComputedRef<Array<{
    value: number;
    label: string;
    description: string | null;
    platform: AdminGroup["platform"];
    subscriptionType: AdminGroup["subscription_type"];
    rate: number;
    [key: string]: unknown;
  }>>;

  // Auth source defaults (used by Users tab)
  authSourceDefaults: Reactive<AuthSourceDefaultsState>;
  authSourceDefaultsMeta: ComputedRef<Array<{
    source: AuthSourceType;
    title: string;
    description: string;
  }>>;

  // Registration email suffix whitelist (used by Security tab)
  registrationEmailSuffixWhitelistTags: Ref<string[]>;

  // Operations
  saveSettings: () => Promise<void>;
  loadSettings: () => Promise<void>;
  loadSubscriptionGroups: () => Promise<void>;
}

const SETTINGS_STATE_KEY: InjectionKey<SettingsStateContext> =
  Symbol("SettingsStateContext");

/**
 * Called from the root SettingsView to provide the shared state
 * to all child panel components.
 */
export function provideSettingsState(state: SettingsStateContext): void {
  provide(SETTINGS_STATE_KEY, state);
}

/**
 * Called from each panel component to inject the shared state.
 * Throws if called outside a SettingsView provider.
 */
export function useSettingsState(): SettingsStateContext {
  const state = inject(SETTINGS_STATE_KEY);
  if (!state) {
    throw new Error(
      "useSettingsState() must be used inside a SettingsView component that calls provideSettingsState().",
    );
  }
  return state;
}
