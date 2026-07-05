<script setup lang="ts">
import { ref, computed, watch, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { useSettingsState } from "@/composables/useSettingsState";
import { useClipboard } from "@/composables/useClipboard";
import { useAppStore } from "@/stores";
import { adminAPI } from "@/api";
import { extractApiErrorMessage } from "@/utils/apiError";
import {
  normalizeRegistrationEmailSuffixDomain,
  isRegistrationEmailSuffixDomainValid,
  parseRegistrationEmailSuffixWhitelistInput,
} from "@/utils/registrationEmailPolicy";
import type { WeChatConnectMode } from "@/api/admin/settings";
import {
  resolveWeChatConnectModeCapabilities,
  deriveWeChatConnectStoredMode,
  defaultWeChatConnectScopesForMode,
} from "@/api/admin/settings";
import Toggle from "@/components/common/Toggle.vue";
import Icon from "@/components/icons/Icon.vue";

const { t } = useI18n();
const {
  form,
  localText,
  isZhLocale,
  buildApiCallbackUrl,
  registrationEmailSuffixWhitelistTags,
} = useSettingsState();
const appStore = useAppStore();
const { copyToClipboard } = useClipboard();

// ── Admin API Key state ──
const adminApiKeyLoading = ref(true);
const adminApiKeyExists = ref(false);
const adminApiKeyMasked = ref("");
const adminApiKeyOperating = ref(false);
const newAdminApiKey = ref("");

// ── Registration email suffix whitelist ──
const registrationEmailSuffixWhitelistDraft = ref("");

const registrationEmailSuffixWhitelistSeparatorKeys = new Set([
  " ",
  ",",
  "，",
  "Enter",
  "Tab",
]);

function removeRegistrationEmailSuffixWhitelistTag(suffix: string) {
  registrationEmailSuffixWhitelistTags.value =
    registrationEmailSuffixWhitelistTags.value.filter(
      (item) => item !== suffix,
    );
}

function addRegistrationEmailSuffixWhitelistTag(raw: string) {
  const suffix = normalizeRegistrationEmailSuffixDomain(raw);
  if (
    !isRegistrationEmailSuffixDomainValid(suffix) ||
    registrationEmailSuffixWhitelistTags.value.includes(suffix)
  ) {
    return;
  }
  registrationEmailSuffixWhitelistTags.value = [
    ...registrationEmailSuffixWhitelistTags.value,
    suffix,
  ];
}

function commitRegistrationEmailSuffixWhitelistDraft() {
  if (!registrationEmailSuffixWhitelistDraft.value) {
    return;
  }
  addRegistrationEmailSuffixWhitelistTag(
    registrationEmailSuffixWhitelistDraft.value,
  );
  registrationEmailSuffixWhitelistDraft.value = "";
}

function handleRegistrationEmailSuffixWhitelistDraftInput() {
  registrationEmailSuffixWhitelistDraft.value =
    normalizeRegistrationEmailSuffixDomain(
      registrationEmailSuffixWhitelistDraft.value,
    );
}

function handleRegistrationEmailSuffixWhitelistDraftKeydown(
  event: KeyboardEvent,
) {
  if (event.isComposing) {
    return;
  }

  if (registrationEmailSuffixWhitelistSeparatorKeys.has(event.key)) {
    event.preventDefault();
    commitRegistrationEmailSuffixWhitelistDraft();
    return;
  }

  if (
    event.key === "Backspace" &&
    !registrationEmailSuffixWhitelistDraft.value &&
    registrationEmailSuffixWhitelistTags.value.length > 0
  ) {
    registrationEmailSuffixWhitelistTags.value.pop();
  }
}

function handleRegistrationEmailSuffixWhitelistPaste(event: ClipboardEvent) {
  const text = event.clipboardData?.getData("text") || "";
  if (!text.trim()) {
    return;
  }
  event.preventDefault();
  const tokens = parseRegistrationEmailSuffixWhitelistInput(text);
  for (const token of tokens) {
    addRegistrationEmailSuffixWhitelistTag(token);
  }
}

// ── OAuth redirect URL helpers ──

const linuxdoRedirectUrlSuggestion = computed(() => {
  return buildApiCallbackUrl("/auth/oauth/linuxdo/callback");
});

async function setAndCopyLinuxdoRedirectUrl() {
  const url = linuxdoRedirectUrlSuggestion.value;
  if (!url) return;

  form.linuxdo_connect_redirect_url = url;
  await copyToClipboard(
    url,
    t("admin.settings.linuxdo.redirectUrlSetAndCopied"),
  );
}

type EmailOAuthProvider = "github" | "google";

const githubOAuthRedirectUrlSuggestion = computed(() => {
  return buildApiCallbackUrl("/auth/oauth/github/callback");
});

const googleOAuthRedirectUrlSuggestion = computed(() => {
  return buildApiCallbackUrl("/auth/oauth/google/callback");
});

async function setAndCopyEmailOAuthRedirectUrl(provider: EmailOAuthProvider) {
  const url =
    provider === "github"
      ? githubOAuthRedirectUrlSuggestion.value
      : googleOAuthRedirectUrlSuggestion.value;
  if (!url) return;

  if (provider === "github") {
    form.github_oauth_redirect_url = url;
  } else {
    form.google_oauth_redirect_url = url;
  }
  await copyToClipboard(
    url,
    localText("回调地址已写入并复制。", "Callback URL set and copied."),
  );
}

// ── WeChat connect ──

const wechatRedirectUrlSuggestion = computed(() => {
  return buildApiCallbackUrl("/auth/oauth/wechat/callback");
});

function syncWeChatConnectMode(preferredMode?: WeChatConnectMode) {
  if (form.wechat_connect_mp_enabled && form.wechat_connect_mobile_enabled) {
    if (preferredMode === "mobile") {
      form.wechat_connect_mp_enabled = false;
    } else {
      form.wechat_connect_mobile_enabled = false;
    }
  }

  const capabilities = resolveWeChatConnectModeCapabilities(
    form.wechat_connect_open_enabled,
    form.wechat_connect_mp_enabled,
    form.wechat_connect_mobile_enabled,
    form.wechat_connect_mode,
  );
  form.wechat_connect_open_enabled = capabilities.openEnabled;
  form.wechat_connect_mp_enabled = capabilities.mpEnabled;
  form.wechat_connect_mobile_enabled = capabilities.mobileEnabled;
  form.wechat_connect_mode = deriveWeChatConnectStoredMode(
    capabilities.openEnabled,
    capabilities.mpEnabled,
    capabilities.mobileEnabled,
    form.wechat_connect_mode,
  );
  form.wechat_connect_scopes = defaultWeChatConnectScopesForMode(
    form.wechat_connect_mode,
  );
}

function handleWeChatOpenEnabledChange(value: boolean) {
  form.wechat_connect_open_enabled = value;
  syncWeChatConnectMode(value ? "open" : undefined);
}

function handleWeChatMPEnabledChange(value: boolean) {
  form.wechat_connect_mp_enabled = value;
  if (value) {
    form.wechat_connect_mobile_enabled = false;
  }
  syncWeChatConnectMode(value ? "mp" : undefined);
}

function handleWeChatMobileEnabledChange(value: boolean) {
  form.wechat_connect_mobile_enabled = value;
  if (value) {
    form.wechat_connect_mp_enabled = false;
  }
  syncWeChatConnectMode(value ? "mobile" : undefined);
}

async function setAndCopyWeChatRedirectUrl() {
  const url = wechatRedirectUrlSuggestion.value;
  if (!url) return;

  form.wechat_connect_redirect_url = url;
  await copyToClipboard(
    url,
    t("admin.settings.wechatConnect.redirectUrlSetAndCopied"),
  );
}

// ── OIDC ──

const oidcRedirectUrlSuggestion = computed(() => {
  return buildApiCallbackUrl("/auth/oauth/oidc/callback");
});

async function setAndCopyOIDCRedirectUrl() {
  const url = oidcRedirectUrlSuggestion.value;
  if (!url) return;

  form.oidc_connect_redirect_url = url;
  await copyToClipboard(url, t("admin.settings.oidc.redirectUrlSetAndCopied"));
}

// ── Admin API Key methods ──

async function loadAdminApiKey() {
  adminApiKeyLoading.value = true;
  try {
    const status = await adminAPI.settings.getAdminApiKey();
    adminApiKeyExists.value = status.exists;
    adminApiKeyMasked.value = status.masked_key;
  } catch (_error: unknown) {
    // Silent fail - admin API key status is non-critical
  } finally {
    adminApiKeyLoading.value = false;
  }
}

async function createAdminApiKey() {
  adminApiKeyOperating.value = true;
  try {
    const result = await adminAPI.settings.regenerateAdminApiKey();
    newAdminApiKey.value = result.key;
    adminApiKeyExists.value = true;
    adminApiKeyMasked.value =
      result.key.substring(0, 10) + "..." + result.key.slice(-4);
    appStore.showSuccess(t("admin.settings.adminApiKey.keyGenerated"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t("common.error")));
  } finally {
    adminApiKeyOperating.value = false;
  }
}

async function regenerateAdminApiKey() {
  if (!confirm(t("admin.settings.adminApiKey.regenerateConfirm"))) return;
  await createAdminApiKey();
}

async function deleteAdminApiKey() {
  if (!confirm(t("admin.settings.adminApiKey.deleteConfirm"))) return;
  adminApiKeyOperating.value = true;
  try {
    await adminAPI.settings.deleteAdminApiKey();
    adminApiKeyExists.value = false;
    adminApiKeyMasked.value = "";
    newAdminApiKey.value = "";
    appStore.showSuccess(t("admin.settings.adminApiKey.keyDeleted"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t("common.error")));
  } finally {
    adminApiKeyOperating.value = false;
  }
}

function copyNewKey() {
  navigator.clipboard
    .writeText(newAdminApiKey.value)
    .then(() => {
      appStore.showSuccess(t("admin.settings.adminApiKey.keyCopied"));
    })
    .catch(() => {
      appStore.showError(t("common.copyFailed"));
    });
}

// ── DingTalk corp restriction policy watch ──
watch(
  () => form.dingtalk_connect_corp_restriction_policy,
  (policy) => {
    if (policy !== "internal_only") {
      if (form.dingtalk_connect_bypass_registration) form.dingtalk_connect_bypass_registration = false;
      if (form.dingtalk_connect_sync_corp_email) form.dingtalk_connect_sync_corp_email = false;
      if (form.dingtalk_connect_sync_display_name) form.dingtalk_connect_sync_display_name = false;
      if (form.dingtalk_connect_sync_dept) form.dingtalk_connect_sync_dept = false;
    }
  },
);

// ── Lifecycle ──
onMounted(() => {
  loadAdminApiKey();
});
</script>

<template>
  <div class="space-y-6">
    <!-- Admin API Key Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.adminApiKey.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.adminApiKey.description") }}
        </p>
      </div>
      <div class="space-y-4 p-6">
        <!-- Security Warning -->
        <div
          class="rounded-lg border border-amber-200 bg-amber-50 p-4 dark:border-amber-800 dark:bg-amber-900/20"
        >
          <div class="flex items-start">
            <Icon
              name="exclamationTriangle"
              size="md"
              class="mt-0.5 flex-shrink-0 text-amber-500"
            />
            <p class="ml-3 text-sm text-amber-700 dark:text-amber-300">
              {{ t("admin.settings.adminApiKey.securityWarning") }}
            </p>
          </div>
        </div>

        <!-- Loading State -->
        <div
          v-if="adminApiKeyLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <!-- No Key Configured -->
        <div
          v-else-if="!adminApiKeyExists"
          class="flex items-center justify-between"
        >
          <span class="text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.adminApiKey.notConfigured") }}
          </span>
          <button
            type="button"
            @click="createAdminApiKey"
            :disabled="adminApiKeyOperating"
            class="btn btn-primary btn-sm"
          >
            <svg
              v-if="adminApiKeyOperating"
              class="mr-1 h-4 w-4 animate-spin"
              fill="none"
              viewBox="0 0 24 24"
            >
              <circle
                class="opacity-25"
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                stroke-width="4"
              ></circle>
              <path
                class="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              ></path>
            </svg>
            {{
              adminApiKeyOperating
                ? t("admin.settings.adminApiKey.creating")
                : t("admin.settings.adminApiKey.create")
            }}
          </button>
        </div>

        <!-- Key Exists -->
        <div v-else class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <label
                class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.adminApiKey.currentKey") }}
              </label>
              <code
                class="rounded bg-gray-100 px-2 py-1 font-mono text-sm text-gray-900 dark:bg-dark-700 dark:text-gray-100"
              >
                {{ adminApiKeyMasked }}
              </code>
            </div>
            <div class="flex gap-2">
              <button
                type="button"
                @click="regenerateAdminApiKey"
                :disabled="adminApiKeyOperating"
                class="btn btn-secondary btn-sm"
              >
                {{
                  adminApiKeyOperating
                    ? t("admin.settings.adminApiKey.regenerating")
                    : t("admin.settings.adminApiKey.regenerate")
                }}
              </button>
              <button
                type="button"
                @click="deleteAdminApiKey"
                :disabled="adminApiKeyOperating"
                class="btn btn-secondary btn-sm text-red-600 hover:text-red-700 dark:text-red-400"
              >
                {{ t("admin.settings.adminApiKey.delete") }}
              </button>
            </div>
          </div>

          <!-- Newly Generated Key Display -->
          <div
            v-if="newAdminApiKey"
            class="space-y-3 rounded-lg border border-green-200 bg-green-50 p-4 dark:border-green-800 dark:bg-green-900/20"
          >
            <p
              class="text-sm font-medium text-green-700 dark:text-green-300"
            >
              {{ t("admin.settings.adminApiKey.keyWarning") }}
            </p>
            <div class="flex items-center gap-2">
              <code
                class="flex-1 select-all break-all rounded border border-green-300 bg-white px-3 py-2 font-mono text-sm dark:border-green-700 dark:bg-dark-800"
              >
                {{ newAdminApiKey }}
              </code>
              <button
                type="button"
                @click="copyNewKey"
                class="btn btn-primary btn-sm flex-shrink-0"
              >
                {{ t("admin.settings.adminApiKey.copyKey") }}
              </button>
            </div>
            <p class="text-xs text-green-600 dark:text-green-400">
              {{ t("admin.settings.adminApiKey.usage") }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <!-- Registration Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.registration.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.registration.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <!-- Enable Registration -->
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.registration.enableRegistration")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{
                t("admin.settings.registration.enableRegistrationHint")
              }}
            </p>
          </div>
          <Toggle v-model="form.registration_enabled" />
        </div>

        <!-- Email Verification -->
        <div
          class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.registration.emailVerification")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.registration.emailVerificationHint") }}
            </p>
          </div>
          <Toggle v-model="form.email_verify_enabled" />
        </div>

        <!-- Email Suffix Whitelist -->
        <div class="border-t border-gray-100 pt-4 dark:border-dark-700">
          <label class="font-medium text-gray-900 dark:text-white">{{
            t("admin.settings.registration.emailSuffixWhitelist")
          }}</label>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{
              t("admin.settings.registration.emailSuffixWhitelistHint")
            }}
          </p>
          <div
            class="mt-3 rounded-lg border border-gray-300 bg-white p-2 dark:border-dark-500 dark:bg-dark-700"
          >
            <div class="flex flex-wrap items-center gap-2">
              <span
                v-for="suffix in registrationEmailSuffixWhitelistTags"
                :key="suffix"
                class="inline-flex items-center gap-1 rounded bg-gray-100 px-2 py-1 text-xs font-mono text-gray-700 dark:bg-dark-600 dark:text-gray-200"
              >
                <span>{{ suffix }}</span>
                <button
                  type="button"
                  class="rounded-full text-gray-500 hover:bg-gray-200 hover:text-gray-700 dark:text-gray-300 dark:hover:bg-dark-500 dark:hover:text-white"
                  @click="
                    removeRegistrationEmailSuffixWhitelistTag(suffix)
                  "
                >
                  <Icon
                    name="x"
                    size="xs"
                    class="h-3.5 w-3.5"
                    :stroke-width="2"
                  />
                </button>
              </span>

              <div
                class="flex min-w-[220px] flex-1 items-center gap-1 rounded border border-transparent px-2 py-1 focus-within:border-primary-300 dark:focus-within:border-primary-700"
              >
                <input
                  v-model="registrationEmailSuffixWhitelistDraft"
                  type="text"
                  class="w-full bg-transparent text-sm font-mono text-gray-900 outline-none placeholder:text-gray-400 dark:text-white dark:placeholder:text-gray-500"
                  :placeholder="
                    t(
                      'admin.settings.registration.emailSuffixWhitelistPlaceholder',
                    )
                  "
                  @input="
                    handleRegistrationEmailSuffixWhitelistDraftInput
                  "
                  @keydown="
                    handleRegistrationEmailSuffixWhitelistDraftKeydown
                  "
                  @blur="commitRegistrationEmailSuffixWhitelistDraft"
                  @paste="handleRegistrationEmailSuffixWhitelistPaste"
                />
              </div>
            </div>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.registration.emailSuffixWhitelistInputHint",
              )
            }}
          </p>
        </div>

        <!-- Promo Code -->
        <div
          class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.registration.promoCode")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.registration.promoCodeHint") }}
            </p>
          </div>
          <Toggle v-model="form.promo_code_enabled" />
        </div>

        <!-- Invitation Code -->
        <div
          class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.registration.invitationCode")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.registration.invitationCodeHint") }}
            </p>
          </div>
          <Toggle v-model="form.invitation_code_enabled" />
        </div>
        <!-- Password Reset - Only show when email verification is enabled -->
        <div
          v-if="form.email_verify_enabled"
          class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.registration.passwordReset")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.registration.passwordResetHint") }}
            </p>
          </div>
          <Toggle v-model="form.password_reset_enabled" />
        </div>
        <!-- Frontend URL - Only show when password reset is enabled -->
        <div
          v-if="form.email_verify_enabled && form.password_reset_enabled"
          class="border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <label
            class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
          >
            {{ t("admin.settings.registration.frontendUrl") }}
          </label>
          <input
            v-model="form.frontend_url"
            type="url"
            class="input"
            :placeholder="
              t('admin.settings.registration.frontendUrlPlaceholder')
            "
          />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.registration.frontendUrlHint") }}
          </p>
        </div>

        <!-- TOTP 2FA -->
        <div
          class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.registration.totp")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.registration.totpHint") }}
            </p>
            <!-- Warning when encryption key not configured -->
            <p
              v-if="!form.totp_encryption_key_configured"
              class="mt-2 text-sm text-amber-600 dark:text-amber-400"
            >
              {{ t("admin.settings.registration.totpKeyNotConfigured") }}
            </p>
          </div>
          <Toggle
            v-model="form.totp_enabled"
            :disabled="!form.totp_encryption_key_configured"
          />
        </div>
      </div>
    </div>

    <!-- API Key IP ACL Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.apiKeyAcl.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.apiKeyAcl.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between gap-4">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">
              {{ t("admin.settings.apiKeyAcl.trustForwardedIp") }}
            </label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.apiKeyAcl.trustForwardedIpHint") }}
            </p>
          </div>
          <Toggle v-model="form.api_key_acl_trust_forwarded_ip" />
        </div>
      </div>
    </div>

    <!-- Cloudflare Turnstile Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.turnstile.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.turnstile.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <!-- Enable Turnstile -->
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.turnstile.enableTurnstile")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.turnstile.enableTurnstileHint") }}
            </p>
          </div>
          <Toggle v-model="form.turnstile_enabled" />
        </div>

        <!-- Turnstile Keys - Only show when enabled -->
        <div
          v-if="form.turnstile_enabled"
          class="border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div class="grid grid-cols-1 gap-6">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.turnstile.siteKey") }}
              </label>
              <input
                v-model="form.turnstile_site_key"
                type="text"
                class="input font-mono text-sm"
                placeholder="0x4AAAAAAA..."
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.turnstile.siteKeyHint") }}
                <a
                  href="https://dash.cloudflare.com/"
                  target="_blank"
                  class="text-primary-600 hover:text-primary-500"
                  >{{
                    t("admin.settings.turnstile.cloudflareDashboard")
                  }}</a
                >
              </p>
            </div>
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.turnstile.secretKey") }}
              </label>
              <input
                v-model="form.turnstile_secret_key"
                type="password"
                class="input font-mono text-sm"
                placeholder="0x4AAAAAAA..."
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  form.turnstile_secret_key_configured
                    ? t(
                        "admin.settings.turnstile.secretKeyConfiguredHint",
                      )
                    : t("admin.settings.turnstile.secretKeyHint")
                }}
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- LinuxDo Connect OAuth -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.linuxdo.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.linuxdo.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.linuxdo.enable")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.linuxdo.enableHint") }}
            </p>
          </div>
          <Toggle v-model="form.linuxdo_connect_enabled" />
        </div>

        <div
          v-if="form.linuxdo_connect_enabled"
          class="border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div class="grid grid-cols-1 gap-6">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.linuxdo.clientId") }}
              </label>
              <input
                v-model="form.linuxdo_connect_client_id"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.linuxdo.clientIdPlaceholder')
                "
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.linuxdo.clientIdHint") }}
              </p>
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.linuxdo.clientSecret") }}
              </label>
              <input
                v-model="form.linuxdo_connect_client_secret"
                type="password"
                class="input font-mono text-sm"
                :placeholder="
                  form.linuxdo_connect_client_secret_configured
                    ? t(
                        'admin.settings.linuxdo.clientSecretConfiguredPlaceholder',
                      )
                    : t('admin.settings.linuxdo.clientSecretPlaceholder')
                "
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  form.linuxdo_connect_client_secret_configured
                    ? t(
                        "admin.settings.linuxdo.clientSecretConfiguredHint",
                      )
                    : t("admin.settings.linuxdo.clientSecretHint")
                }}
              </p>
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.linuxdo.redirectUrl") }}
              </label>
              <input
                v-model="form.linuxdo_connect_redirect_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.linuxdo.redirectUrlPlaceholder')
                "
              />
              <div
                class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3"
              >
                <button
                  type="button"
                  class="btn btn-secondary btn-sm w-fit"
                  @click="setAndCopyLinuxdoRedirectUrl"
                >
                  {{ t("admin.settings.linuxdo.quickSetCopy") }}
                </button>
                <code
                  v-if="linuxdoRedirectUrlSuggestion"
                  class="select-all break-all rounded bg-gray-50 px-2 py-1 font-mono text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
                >
                  {{ linuxdoRedirectUrlSuggestion }}
                </code>
              </div>
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.linuxdo.redirectUrlHint") }}
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- GitHub / Google Email OAuth Sign-in -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ localText("邮箱快捷登录", "Email OAuth Sign-in") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{
            localText(
              "开启 GitHub 或 Google 邮箱授权登录后，系统会读取已验证邮箱，存在则直接登录，不存在则自动注册。",
              "After GitHub or Google email OAuth is enabled, the system reads a verified email, signs in matching users, and auto-registers missing users.",
            )
          }}
        </p>
      </div>
      <div class="space-y-6 p-6">
        <div class="grid grid-cols-1 gap-6 xl:grid-cols-2">
          <div
            class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
          >
            <div class="flex items-start justify-between gap-4">
              <div>
                <h3 class="font-medium text-gray-900 dark:text-white">
                  GitHub
                </h3>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                  {{
                    localText(
                      "GitHub OAuth App 需要 read:user user:email 权限，回调地址填写下方后端地址。",
                      "GitHub OAuth App needs read:user user:email scopes. Use the backend callback URL below.",
                    )
                  }}
                </p>
              </div>
              <Toggle v-model="form.github_oauth_enabled" />
            </div>

            <div v-if="form.github_oauth_enabled" class="mt-4 space-y-4">
              <div
                class="rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
              >
                <template v-if="isZhLocale">
                  开通引导：GitHub Settings → Developer settings →
                  <a
                    data-testid="github-oauth-apps-guide-link"
                    href="https://github.com/settings/developers"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="font-medium text-primary-600 hover:underline dark:text-primary-400"
                    >OAuth Apps</a
                  >
                  → New OAuth App；Homepage URL 填站点域名，Authorization callback URL 填下面的后端回调地址。
                </template>
                <template v-else>
                  Setup guide: GitHub Settings &rarr; Developer settings &rarr;
                  <a
                    data-testid="github-oauth-apps-guide-link"
                    href="https://github.com/settings/developers"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="font-medium text-primary-600 hover:underline dark:text-primary-400"
                    >OAuth Apps</a
                  >
                  &rarr; New OAuth App. Use your site origin as Homepage URL and the backend callback URL below as Authorization callback URL.
                </template>
              </div>

              <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                    >Client ID</label
                  >
                  <input
                    v-model="form.github_oauth_client_id"
                    type="text"
                    class="input font-mono text-sm"
                    placeholder="GitHub OAuth Client ID"
                  />
                </div>
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                    >Client Secret</label
                  >
                  <input
                    v-model="form.github_oauth_client_secret"
                    type="password"
                    class="input font-mono text-sm"
                    :placeholder="
                      form.github_oauth_client_secret_configured
                        ? localText(
                            '密钥已配置，留空以保留当前值。',
                            'Secret configured. Leave empty to keep the current value.',
                          )
                        : 'GitHub OAuth Client Secret'
                    "
                  />
                </div>
              </div>

              <div>
                <label
                  class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                >
                  {{ localText("后端回调地址", "Backend Callback URL") }}
                </label>
                <input
                  v-model="form.github_oauth_redirect_url"
                  type="url"
                  class="input font-mono text-sm"
                  placeholder="https://your-domain.com/api/v1/auth/oauth/github/callback"
                />
                <div
                  class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3"
                >
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm w-fit"
                    @click="setAndCopyEmailOAuthRedirectUrl('github')"
                  >
                    {{ localText("生成并复制", "Generate and copy") }}
                  </button>
                  <code
                    v-if="githubOAuthRedirectUrlSuggestion"
                    class="select-all break-all rounded bg-gray-50 px-2 py-1 font-mono text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
                  >
                    {{ githubOAuthRedirectUrlSuggestion }}
                  </code>
                </div>
              </div>

              <div>
                <label
                  class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                >
                  {{ localText("前端回跳地址", "Frontend Callback URL") }}
                </label>
                <input
                  v-model="form.github_oauth_frontend_redirect_url"
                  type="text"
                  class="input font-mono text-sm"
                  placeholder="/auth/oauth/callback"
                />
              </div>
            </div>
          </div>

          <div
            class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
          >
            <div class="flex items-start justify-between gap-4">
              <div>
                <h3 class="font-medium text-gray-900 dark:text-white">
                  Google
                </h3>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                  {{
                    localText(
                      "Google OAuth 客户端需要 openid email profile 范围，并在凭据里登记后端回调地址。",
                      "Google OAuth client needs openid email profile scopes and the backend callback URL registered in credentials.",
                    )
                  }}
                </p>
              </div>
              <Toggle v-model="form.google_oauth_enabled" />
            </div>

            <div v-if="form.google_oauth_enabled" class="mt-4 space-y-4">
              <div
                class="rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
              >
                {{
                  localText(
                    "开通引导：Google Cloud Console → APIs & Services → OAuth consent screen 完成同意屏幕；Credentials → Create Credentials → OAuth client ID，类型选择 Web application，并把下面地址加入 Authorized redirect URIs。",
                    "Setup guide: Google Cloud Console → APIs & Services → OAuth consent screen, then Credentials → Create Credentials → OAuth client ID, choose Web application, and add the URL below to Authorized redirect URIs.",
                  )
                }}
              </div>

              <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                    >Client ID</label
                  >
                  <input
                    v-model="form.google_oauth_client_id"
                    type="text"
                    class="input font-mono text-sm"
                    placeholder="Google OAuth Client ID"
                  />
                </div>
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                    >Client Secret</label
                  >
                  <input
                    v-model="form.google_oauth_client_secret"
                    type="password"
                    class="input font-mono text-sm"
                    :placeholder="
                      form.google_oauth_client_secret_configured
                        ? localText(
                            '密钥已配置，留空以保留当前值。',
                            'Secret configured. Leave empty to keep the current value.',
                          )
                        : 'Google OAuth Client Secret'
                    "
                  />
                </div>
              </div>

              <div>
                <label
                  class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                >
                  {{ localText("后端回调地址", "Backend Callback URL") }}
                </label>
                <input
                  v-model="form.google_oauth_redirect_url"
                  type="url"
                  class="input font-mono text-sm"
                  placeholder="https://your-domain.com/api/v1/auth/oauth/google/callback"
                />
                <div
                  class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3"
                >
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm w-fit"
                    @click="setAndCopyEmailOAuthRedirectUrl('google')"
                  >
                    {{ localText("生成并复制", "Generate and copy") }}
                  </button>
                  <code
                    v-if="googleOAuthRedirectUrlSuggestion"
                    class="select-all break-all rounded bg-gray-50 px-2 py-1 font-mono text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
                  >
                    {{ googleOAuthRedirectUrlSuggestion }}
                  </code>
                </div>
              </div>

              <div>
                <label
                  class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                >
                  {{ localText("前端回跳地址", "Frontend Callback URL") }}
                </label>
                <input
                  v-model="form.google_oauth_frontend_redirect_url"
                  type="text"
                  class="input font-mono text-sm"
                  placeholder="/auth/oauth/callback"
                />
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- WeChat Connect OAuth -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.wechatConnect.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.wechatConnect.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.wechatConnect.enabledLabel")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.wechatConnect.enabledHint") }}
            </p>
          </div>
          <Toggle
            v-model="form.wechat_connect_enabled"
            data-testid="wechat-connect-enabled"
          />
        </div>

        <div
          v-if="form.wechat_connect_enabled"
          class="space-y-6 border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div class="space-y-4">
            <div
              class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
            >
              <div class="flex items-start justify-between gap-4">
                <div>
                  <h3 class="font-medium text-gray-900 dark:text-white">
                    {{ localText("PC 应用", "PC App") }}
                  </h3>
                  <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                    {{
                      localText(
                        "桌面浏览器通过微信开放平台扫码登录。可与公众号或移动应用同时存在。",
                        "Desktop browsers sign in through WeChat Open Platform QR login. This can coexist with Official Account or Mobile App.",
                      )
                    }}
                  </p>
                </div>
                <Toggle
                  :model-value="form.wechat_connect_open_enabled"
                  data-testid="wechat-connect-open-enabled"
                  @update:model-value="handleWeChatOpenEnabledChange"
                />
              </div>
              <div
                v-if="form.wechat_connect_open_enabled"
                class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-2"
              >
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                  >
                    {{ localText("PC AppID", "PC App ID") }}
                  </label>
                  <input
                    v-model="form.wechat_connect_open_app_id"
                    data-testid="wechat-connect-open-app-id"
                    type="text"
                    class="input font-mono text-sm"
                    :placeholder="
                      localText(
                        '微信开放平台 PC 应用 AppID',
                        'WeChat Open Platform PC App ID',
                      )
                    "
                  />
                </div>
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                  >
                    {{ localText("PC AppSecret", "PC App Secret") }}
                  </label>
                  <input
                    v-model="form.wechat_connect_open_app_secret"
                    data-testid="wechat-connect-open-app-secret"
                    type="password"
                    class="input font-mono text-sm"
                    :placeholder="
                      form.wechat_connect_open_app_secret_configured
                        ? localText(
                            '密钥已配置，留空以保留当前值。',
                            'Secret configured. Leave empty to keep the current value.',
                          )
                        : localText(
                            '微信开放平台 PC 应用 AppSecret',
                            'WeChat Open Platform PC App Secret',
                          )
                    "
                  />
                </div>
              </div>
            </div>

            <div
              class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
            >
              <div class="flex items-start justify-between gap-4">
                <div>
                  <h3 class="font-medium text-gray-900 dark:text-white">
                    {{ localText("公众号", "Official Account") }}
                  </h3>
                  <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                    {{
                      localText(
                        "仅在微信内浏览器可用；非微信环境下会显示不可用。",
                        "Only available inside the WeChat browser. It is shown as unavailable outside WeChat.",
                      )
                    }}
                  </p>
                </div>
                <Toggle
                  :model-value="form.wechat_connect_mp_enabled"
                  data-testid="wechat-connect-mp-enabled"
                  @update:model-value="handleWeChatMPEnabledChange"
                />
              </div>
              <div
                v-if="form.wechat_connect_mp_enabled"
                class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-2"
              >
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                  >
                    {{ localText("公众号 AppID", "Official Account App ID") }}
                  </label>
                  <input
                    v-model="form.wechat_connect_mp_app_id"
                    data-testid="wechat-connect-mp-app-id"
                    type="text"
                    class="input font-mono text-sm"
                    :placeholder="
                      localText(
                        '公众号 AppID',
                        'Official Account App ID',
                      )
                    "
                  />
                </div>
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                  >
                    {{
                      localText(
                        "公众号 AppSecret",
                        "Official Account App Secret",
                      )
                    }}
                  </label>
                  <input
                    v-model="form.wechat_connect_mp_app_secret"
                    data-testid="wechat-connect-mp-app-secret"
                    type="password"
                    class="input font-mono text-sm"
                    :placeholder="
                      form.wechat_connect_mp_app_secret_configured
                        ? localText(
                            '密钥已配置，留空以保留当前值。',
                            'Secret configured. Leave empty to keep the current value.',
                          )
                        : localText(
                            '公众号 AppSecret',
                            'Official Account App Secret',
                          )
                    "
                  />
                </div>
              </div>
            </div>

            <div
              class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
            >
              <div class="flex items-start justify-between gap-4">
                <div>
                  <h3 class="font-medium text-gray-900 dark:text-white">
                    {{ localText("移动应用", "Mobile App") }}
                  </h3>
                  <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                    {{
                      localText(
                        "原生移动端通过微信 SDK 唤起授权，网页端不会直接发起该流程。",
                        "Native mobile clients start authorization through the WeChat SDK. The web UI does not launch this flow directly.",
                      )
                    }}
                  </p>
                </div>
                <Toggle
                  :model-value="form.wechat_connect_mobile_enabled"
                  data-testid="wechat-connect-mobile-enabled"
                  @update:model-value="handleWeChatMobileEnabledChange"
                />
              </div>
              <div
                v-if="form.wechat_connect_mobile_enabled"
                class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-2"
              >
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                  >
                    {{ localText("移动应用 AppID", "Mobile App ID") }}
                  </label>
                  <input
                    v-model="form.wechat_connect_mobile_app_id"
                    data-testid="wechat-connect-mobile-app-id"
                    type="text"
                    class="input font-mono text-sm"
                    :placeholder="
                      localText(
                        '移动应用 AppID',
                        'Mobile App ID',
                      )
                    "
                  />
                </div>
                <div>
                  <label
                    class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                  >
                    {{ localText("移动应用 AppSecret", "Mobile App Secret") }}
                  </label>
                  <input
                    v-model="form.wechat_connect_mobile_app_secret"
                    data-testid="wechat-connect-mobile-app-secret"
                    type="password"
                    class="input font-mono text-sm"
                    :placeholder="
                      form.wechat_connect_mobile_app_secret_configured
                        ? localText(
                            '密钥已配置，留空以保留当前值。',
                            'Secret configured. Leave empty to keep the current value.',
                          )
                        : localText(
                            '移动应用 AppSecret',
                            'Mobile App Secret',
                          )
                    "
                  />
                </div>
              </div>
            </div>
          </div>

          <div
            v-if="
              form.wechat_connect_open_enabled &&
              (form.wechat_connect_mp_enabled ||
                form.wechat_connect_mobile_enabled)
            "
            class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700 dark:border-amber-900/40 dark:bg-amber-900/10 dark:text-amber-300"
          >
            {{
              localText(
                "如果同时启用 PC 应用和公众号/移动应用，这些应用需要挂在同一个微信开放平台主体下，否则 UnionID 无法稳定归并账号。",
                "When PC App is enabled together with Official Account or Mobile App, they should belong to the same WeChat Open Platform account so UnionID can merge identities reliably.",
              )
            }}
          </div>

          <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{
                  localText(
                    "浏览器回调地址",
                    "Browser Redirect URL",
                  )
                }}
              </label>
              <input
                data-testid="wechat-connect-redirect-url"
                v-model="form.wechat_connect_redirect_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="t('admin.settings.wechatConnect.redirectUrlPlaceholder')"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  localText(
                    "用于 PC 应用和公众号的网页回调。移动应用走原生 SDK 时不直接使用这个浏览器回调。",
                    "Used by PC App and Official Account browser callbacks. Native mobile SDK flows do not start from this browser callback directly.",
                  )
                }}
              </p>
              <div
                class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3"
              >
                <button
                  type="button"
                  class="btn btn-secondary btn-sm w-fit"
                  @click="setAndCopyWeChatRedirectUrl"
                >
                  {{ t("admin.settings.wechatConnect.generateAndCopy") }}
                </button>
                <code
                  v-if="wechatRedirectUrlSuggestion"
                  class="select-all break-all rounded bg-gray-50 px-2 py-1 font-mono text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
                >
                  {{ wechatRedirectUrlSuggestion }}
                </code>
              </div>
            </div>
          </div>

          <div>
            <label
              class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.wechatConnect.frontendRedirectUrlLabel") }}
            </label>
            <input
              data-testid="wechat-connect-frontend-redirect-url"
              v-model="form.wechat_connect_frontend_redirect_url"
              type="text"
              class="input font-mono text-sm"
              :placeholder="t('admin.settings.wechatConnect.frontendRedirectUrlPlaceholder')"
            />
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.wechatConnect.frontendRedirectUrlHint") }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <!-- DingTalk Connect OAuth -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.dingtalk.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.dingtalk.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.dingtalk.enable")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.dingtalk.enableHint") }}
            </p>
          </div>
          <Toggle v-model="form.dingtalk_connect_enabled" />
        </div>

        <div
          v-if="form.dingtalk_connect_enabled"
          class="border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div class="grid grid-cols-1 gap-6">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.dingtalk.clientId") }}
              </label>
              <input
                v-model="form.dingtalk_connect_client_id"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.dingtalk.clientIdPlaceholder')
                "
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.dingtalk.clientIdHint") }}
              </p>
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.dingtalk.clientSecret") }}
              </label>
              <input
                v-model="form.dingtalk_connect_client_secret"
                type="password"
                class="input font-mono text-sm"
                :placeholder="
                  form.dingtalk_connect_client_secret_configured
                    ? t(
                        'admin.settings.dingtalk.clientSecretConfiguredPlaceholder',
                      )
                    : t('admin.settings.dingtalk.clientSecretPlaceholder')
                "
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  form.dingtalk_connect_client_secret_configured
                    ? t(
                        "admin.settings.dingtalk.clientSecretConfiguredHint",
                      )
                    : t("admin.settings.dingtalk.clientSecretHint")
                }}
              </p>
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.dingtalk.redirectUrl") }}
              </label>
              <input
                v-model="form.dingtalk_connect_redirect_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.dingtalk.redirectUrlPlaceholder')
                "
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.dingtalk.redirectUrlHint") }}
              </p>
            </div>

            <!-- Corp Restriction Policy -->
            <div class="border-t border-gray-100 pt-4 dark:border-dark-700">
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.dingtalk.corpPolicy.label") }}
              </label>
              <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.dingtalk.corpPolicy.hint") }}
              </p>
              <div class="space-y-2">
                <label class="flex cursor-pointer items-center gap-3">
                  <input
                    v-model="form.dingtalk_connect_corp_restriction_policy"
                    type="radio"
                    value="none"
                    class="h-4 w-4 text-primary-600"
                  />
                  <span class="text-sm text-gray-700 dark:text-gray-300">
                    {{ t("admin.settings.dingtalk.corpPolicy.none") }}
                  </span>
                </label>
                <label class="flex cursor-pointer items-center gap-3">
                  <input
                    v-model="form.dingtalk_connect_corp_restriction_policy"
                    type="radio"
                    value="internal_only"
                    class="h-4 w-4 text-primary-600"
                  />
                  <span class="text-sm text-gray-700 dark:text-gray-300">
                    {{ t("admin.settings.dingtalk.corpPolicy.internalOnly") }}
                  </span>
                </label>
              </div>
            </div>

            <!-- bypass_registration toggle -->
            <div
              v-if="form.dingtalk_connect_corp_restriction_policy === 'internal_only'"
              class="flex items-center justify-between pt-4 border-t border-gray-100 dark:border-dark-700"
            >
              <div>
                <label class="font-medium text-gray-900 dark:text-white">{{
                  t("admin.settings.dingtalk.bypassRegistration")
                }}</label>
                <p class="text-sm text-gray-500 dark:text-gray-400">
                  {{ t("admin.settings.dingtalk.bypassRegistrationHint") }}
                </p>
              </div>
              <Toggle v-model="form.dingtalk_connect_bypass_registration" />
            </div>

            <!-- Identity sync toggles (internal_only only) -->
            <div
              v-if="form.dingtalk_connect_corp_restriction_policy === 'internal_only'"
              class="pt-4 border-t border-gray-100 dark:border-dark-700 space-y-2"
            >
              <div class="flex items-center justify-between">
                <div>
                  <label class="font-medium text-gray-900 dark:text-white">{{
                    t("admin.settings.dingtalk.syncDisplayName")
                  }}</label>
                  <p class="text-sm text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.dingtalk.syncDisplayNameHint") }}
                  </p>
                </div>
                <Toggle v-model="form.dingtalk_connect_sync_display_name" />
              </div>
              <div v-if="form.dingtalk_connect_sync_display_name" class="space-y-2">
                <div class="flex items-center gap-2">
                  <label class="text-sm text-gray-600 dark:text-gray-400 whitespace-nowrap min-w-[5rem]">
                    {{ t("admin.settings.dingtalk.syncDisplayNameTarget") }}
                  </label>
                  <input
                    v-model="form.dingtalk_connect_sync_display_name_attr_key"
                    type="text"
                    placeholder="dingtalk_name"
                    class="input text-sm flex-1 max-w-xs"
                  />
                </div>
                <div class="flex items-center gap-2">
                  <label class="text-sm text-gray-600 dark:text-gray-400 whitespace-nowrap min-w-[5rem]">
                    {{ t("admin.settings.dingtalk.syncAttrDisplayName") }}
                  </label>
                  <input
                    v-model="form.dingtalk_connect_sync_display_name_attr_name"
                    type="text"
                    :placeholder="localText('钉钉姓名', 'DingTalk Name')"
                    class="input text-sm flex-1 max-w-xs"
                  />
                </div>
              </div>
              <p v-if="form.dingtalk_connect_sync_display_name" class="text-xs text-gray-400 dark:text-gray-500">
                {{ t("admin.settings.dingtalk.syncDisplayNameTargetHint") }}
              </p>
            </div>
            <div
              v-if="form.dingtalk_connect_corp_restriction_policy === 'internal_only'"
              class="pt-4 border-t border-gray-100 dark:border-dark-700 space-y-2"
            >
              <div class="flex items-center justify-between">
                <div>
                  <label class="font-medium text-gray-900 dark:text-white">{{
                    t("admin.settings.dingtalk.syncCorpEmail")
                  }}</label>
                  <p class="text-sm text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.dingtalk.syncCorpEmailHint") }}
                  </p>
                  <p class="text-xs text-amber-600 dark:text-amber-400 mt-1">
                    {{ t("admin.settings.dingtalk.syncCorpEmailPermissionHint") }}
                  </p>
                </div>
                <Toggle v-model="form.dingtalk_connect_sync_corp_email" />
              </div>
              <div v-if="form.dingtalk_connect_sync_corp_email" class="space-y-2">
                <div class="flex items-center gap-2">
                  <label class="text-sm text-gray-600 dark:text-gray-400 whitespace-nowrap min-w-[5rem]">
                    {{ t("admin.settings.dingtalk.syncCorpEmailTarget") }}
                  </label>
                  <input
                    v-model="form.dingtalk_connect_sync_corp_email_attr_key"
                    type="text"
                    placeholder="dingtalk_email"
                    class="input text-sm flex-1 max-w-xs"
                  />
                </div>
                <div class="flex items-center gap-2">
                  <label class="text-sm text-gray-600 dark:text-gray-400 whitespace-nowrap min-w-[5rem]">
                    {{ t("admin.settings.dingtalk.syncAttrDisplayName") }}
                  </label>
                  <input
                    v-model="form.dingtalk_connect_sync_corp_email_attr_name"
                    type="text"
                    :placeholder="localText('钉钉企业邮箱', 'DingTalk Corporate Email')"
                    class="input text-sm flex-1 max-w-xs"
                  />
                </div>
              </div>
              <p v-if="form.dingtalk_connect_sync_corp_email" class="text-xs text-gray-400 dark:text-gray-500">
                {{ t("admin.settings.dingtalk.syncCorpEmailTargetHint") }}
              </p>
            </div>
            <div
              v-if="form.dingtalk_connect_corp_restriction_policy === 'internal_only'"
              class="pt-4 border-t border-gray-100 dark:border-dark-700 space-y-2"
            >
              <div class="flex items-center justify-between">
                <div>
                  <label class="font-medium text-gray-900 dark:text-white">{{
                    t("admin.settings.dingtalk.syncDept")
                  }}</label>
                  <p class="text-sm text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.dingtalk.syncDeptHint") }}
                  </p>
                  <p class="text-xs text-amber-600 dark:text-amber-400 mt-1">
                    {{ t("admin.settings.dingtalk.syncDeptPermissionHint") }}
                  </p>
                </div>
                <Toggle v-model="form.dingtalk_connect_sync_dept" />
              </div>
              <div v-if="form.dingtalk_connect_sync_dept" class="space-y-2">
                <div class="flex items-center gap-2">
                  <label class="text-sm text-gray-600 dark:text-gray-400 whitespace-nowrap min-w-[5rem]">
                    {{ t("admin.settings.dingtalk.syncDeptTarget") }}
                  </label>
                  <input
                    v-model="form.dingtalk_connect_sync_dept_attr_key"
                    type="text"
                    placeholder="dingtalk_department"
                    class="input text-sm flex-1 max-w-xs"
                  />
                </div>
                <div class="flex items-center gap-2">
                  <label class="text-sm text-gray-600 dark:text-gray-400 whitespace-nowrap min-w-[5rem]">
                    {{ t("admin.settings.dingtalk.syncAttrDisplayName") }}
                  </label>
                  <input
                    v-model="form.dingtalk_connect_sync_dept_attr_name"
                    type="text"
                    :placeholder="localText('钉钉部门', 'DingTalk Department')"
                    class="input text-sm flex-1 max-w-xs"
                  />
                </div>
              </div>
              <p v-if="form.dingtalk_connect_sync_dept" class="text-xs text-gray-400 dark:text-gray-500">
                {{ t("admin.settings.dingtalk.syncDeptTargetHint") }}
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Generic OIDC OAuth -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.oidc.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.oidc.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.oidc.enable")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.oidc.enableHint") }}
            </p>
          </div>
          <Toggle v-model="form.oidc_connect_enabled" />
        </div>

        <div
          v-if="form.oidc_connect_enabled"
          class="space-y-6 border-t border-gray-100 pt-4 dark:border-dark-700"
        >
          <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.providerName") }}
              </label>
              <input
                v-model="form.oidc_connect_provider_name"
                type="text"
                class="input"
                :placeholder="
                  t('admin.settings.oidc.providerNamePlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.clientId") }}
              </label>
              <input
                v-model="form.oidc_connect_client_id"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.clientIdPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.clientSecret") }}
              </label>
              <input
                v-model="form.oidc_connect_client_secret"
                type="password"
                class="input font-mono text-sm"
                :placeholder="
                  form.oidc_connect_client_secret_configured
                    ? t(
                        'admin.settings.oidc.clientSecretConfiguredPlaceholder',
                      )
                    : t('admin.settings.oidc.clientSecretPlaceholder')
                "
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  form.oidc_connect_client_secret_configured
                    ? t("admin.settings.oidc.clientSecretConfiguredHint")
                    : t("admin.settings.oidc.clientSecretHint")
                }}
              </p>
            </div>
          </div>

          <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.issuerUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_issuer_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.issuerUrlPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.discoveryUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_discovery_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.discoveryUrlPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.authorizeUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_authorize_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.authorizeUrlPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.tokenUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_token_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.tokenUrlPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.userinfoUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_userinfo_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.userinfoUrlPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.jwksUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_jwks_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="t('admin.settings.oidc.jwksUrlPlaceholder')"
              />
            </div>
          </div>

          <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.scopes") }}
              </label>
              <input
                v-model="form.oidc_connect_scopes"
                type="text"
                class="input font-mono text-sm"
                :placeholder="t('admin.settings.oidc.scopesPlaceholder')"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.oidc.scopesHint") }}
              </p>
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.redirectUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_redirect_url"
                type="url"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.redirectUrlPlaceholder')
                "
              />
              <div
                class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3"
              >
                <button
                  type="button"
                  class="btn btn-secondary btn-sm w-fit"
                  @click="setAndCopyOIDCRedirectUrl"
                >
                  {{ t("admin.settings.oidc.quickSetCopy") }}
                </button>
                <code
                  v-if="oidcRedirectUrlSuggestion"
                  class="select-all break-all rounded bg-gray-50 px-2 py-1 font-mono text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
                >
                  {{ oidcRedirectUrlSuggestion }}
                </code>
              </div>
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.oidc.redirectUrlHint") }}
              </p>
            </div>

            <div class="lg:col-span-2">
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.frontendRedirectUrl") }}
              </label>
              <input
                v-model="form.oidc_connect_frontend_redirect_url"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.frontendRedirectUrlPlaceholder')
                "
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.oidc.frontendRedirectUrlHint") }}
              </p>
            </div>
          </div>

          <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.tokenAuthMethod") }}
              </label>
              <select
                v-model="form.oidc_connect_token_auth_method"
                class="input font-mono text-sm"
              >
                <option value="client_secret_post">
                  client_secret_post
                </option>
                <option value="client_secret_basic">
                  client_secret_basic
                </option>
                <option value="none">none</option>
              </select>
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.clockSkewSeconds") }}
              </label>
              <input
                v-model.number="form.oidc_connect_clock_skew_seconds"
                type="number"
                min="0"
                max="600"
                class="input"
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.allowedSigningAlgs") }}
              </label>
              <input
                v-model="form.oidc_connect_allowed_signing_algs"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.allowedSigningAlgsPlaceholder')
                "
              />
            </div>
          </div>

          <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
            <div
              class="flex items-center justify-between rounded border border-gray-200 px-4 py-3 dark:border-dark-700"
            >
              <div>
                <label class="font-medium text-gray-900 dark:text-white">
                  {{ t("admin.settings.oidc.usePkce") }}
                </label>
              </div>
              <Toggle
                v-model="form.oidc_connect_use_pkce"
                data-testid="oidc-connect-use-pkce"
              />
            </div>

            <div
              class="flex items-center justify-between rounded border border-gray-200 px-4 py-3 dark:border-dark-700"
            >
              <div>
                <label class="font-medium text-gray-900 dark:text-white">
                  {{ t("admin.settings.oidc.validateIdToken") }}
                </label>
              </div>
              <Toggle
                v-model="form.oidc_connect_validate_id_token"
                data-testid="oidc-connect-validate-id-token"
              />
            </div>

            <div
              class="flex items-center justify-between rounded border border-gray-200 px-4 py-3 dark:border-dark-700"
            >
              <div>
                <label class="font-medium text-gray-900 dark:text-white">
                  {{ t("admin.settings.oidc.requireEmailVerified") }}
                </label>
              </div>
              <Toggle
                v-model="form.oidc_connect_require_email_verified"
              />
            </div>
          </div>

          <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.userinfoEmailPath") }}
              </label>
              <input
                v-model="form.oidc_connect_userinfo_email_path"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.userinfoEmailPathPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.userinfoIdPath") }}
              </label>
              <input
                v-model="form.oidc_connect_userinfo_id_path"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.userinfoIdPathPlaceholder')
                "
              />
            </div>

            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.oidc.userinfoUsernamePath") }}
              </label>
              <input
                v-model="form.oidc_connect_userinfo_username_path"
                type="text"
                class="input font-mono text-sm"
                :placeholder="
                  t('admin.settings.oidc.userinfoUsernamePathPlaceholder')
                "
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
