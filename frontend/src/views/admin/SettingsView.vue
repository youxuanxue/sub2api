<template>
    <div class="mx-auto max-w-6xl space-y-6">
      <!-- Loading State -->
      <div v-if="loading" class="flex items-center justify-center py-12">
        <div
          class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"
        ></div>
      </div>

      <!-- Settings Form -->
      <form v-else @submit.prevent="saveSettings" class="space-y-6" novalidate>
        <!-- Tab Navigation -->
        <div class="settings-tabs-shell">
          <nav
            class="settings-tabs-scroll"
            role="tablist"
            :aria-label="t('admin.settings.title')"
          >
            <div class="settings-tabs">
              <button
                v-for="tab in settingsTabs"
                :key="tab.key"
                :id="`settings-tab-${tab.key}`"
                type="button"
                role="tab"
                :aria-selected="activeTab === tab.key"
                :tabindex="activeTab === tab.key ? 0 : -1"
                :class="[
                  'settings-tab',
                  activeTab === tab.key && 'settings-tab-active',
                ]"
                @click="selectSettingsTab(tab.key)"
                @keydown="handleSettingsTabKeydown($event, tab.key)"
              >
                <span class="settings-tab-icon">
                  <Icon :name="tab.icon" size="sm" />
                </span>
                <span class="settings-tab-label">{{
                  t(`admin.settings.tabs.${tab.key}`)
                }}</span>
              </button>
            </div>
          </nav>
        </div>

        <!-- Tab Panels -->
        <div v-show="activeTab === 'security'" class="space-y-6">
          <SecurityPanel />
        </div>

        <div v-show="activeTab === 'gateway'" class="space-y-6">
          <GatewayPanel ref="gatewayPanelRef" />
        </div>

        <!-- Tab: Security — Registration, Turnstile, LinuxDo -->
        <div v-show="activeTab === 'security'" class="space-y-6">
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

              <!-- 会话 IP/UA 绑定 -->
              <div
                class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700"
              >
                <div>
                  <label class="font-medium text-gray-900 dark:text-white">{{
                    t("admin.settings.security.sessionBinding")
                  }}</label>
                  <p class="text-sm text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.security.sessionBindingHint") }}
                  </p>
                </div>
                <Toggle v-model="form.session_binding_enabled" />
              </div>

              <!-- 审计日志保留天数 -->
              <div
                class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700"
              >
                <div>
                  <label class="font-medium text-gray-900 dark:text-white">{{
                    t("admin.settings.security.auditRetention")
                  }}</label>
                  <p class="text-sm text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.security.auditRetentionHint") }}
                  </p>
                </div>
                <input
                  v-model.number="form.audit_log_retention_days"
                  type="number"
                  min="0"
                  class="input w-28 text-right"
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

          <!-- LinuxDo Connect OAuth 登录 -->
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

          <!-- GitHub / Google 邮箱快捷登录 -->
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
                <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
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
                    <div class="rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300">
                      <template v-if="isZhLocale">
                        开通引导：GitHub Settings → Developer settings →
                        <a
                          data-testid="github-oauth-apps-guide-link"
                          href="https://github.com/settings/developers"
                          target="_blank"
                          rel="noopener noreferrer"
                          class="font-medium text-primary-600 hover:underline dark:text-primary-400"
                        >OAuth Apps</a>
                        → New OAuth App；Homepage URL 填站点域名，Authorization callback URL 填下面的后端回调地址。
                      </template>
                      <template v-else>
                        Setup guide: GitHub Settings → Developer settings →
                        <a
                          data-testid="github-oauth-apps-guide-link"
                          href="https://github.com/settings/developers"
                          target="_blank"
                          rel="noopener noreferrer"
                          class="font-medium text-primary-600 hover:underline dark:text-primary-400"
                        >OAuth Apps</a>
                        → New OAuth App. Use your site origin as Homepage URL and the backend callback URL below as Authorization callback URL.
                      </template>
                    </div>

                    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                      <div>
                        <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">Client ID</label>
                        <input
                          v-model="form.github_oauth_client_id"
                          type="text"
                          class="input font-mono text-sm"
                          placeholder="GitHub OAuth Client ID"
                        />
                      </div>
                      <div>
                        <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">Client Secret</label>
                        <input
                          v-model="form.github_oauth_client_secret"
                          type="password"
                          class="input font-mono text-sm"
                          :placeholder="
                            form.github_oauth_client_secret_configured
                              ? localText('密钥已配置，留空以保留当前值。', 'Secret configured. Leave empty to keep the current value.')
                              : 'GitHub OAuth Client Secret'
                          "
                        />
                      </div>
                    </div>

                    <div>
                      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                        {{ localText("后端回调地址", "Backend Callback URL") }}
                      </label>
                      <input
                        v-model="form.github_oauth_redirect_url"
                        type="url"
                        class="input font-mono text-sm"
                        placeholder="https://your-domain.com/api/v1/auth/oauth/github/callback"
                      />
                      <div class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3">
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
                      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
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

                <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
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
                    <div class="rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300">
                      {{
                        localText(
                          "开通引导：Google Cloud Console → APIs & Services → OAuth consent screen 完成同意屏幕；Credentials → Create Credentials → OAuth client ID，类型选择 Web application，并把下面地址加入 Authorized redirect URIs。",
                          "Setup guide: Google Cloud Console → APIs & Services → OAuth consent screen, then Credentials → Create Credentials → OAuth client ID, choose Web application, and add the URL below to Authorized redirect URIs.",
                        )
                      }}
                    </div>

                    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                      <div>
                        <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">Client ID</label>
                        <input
                          v-model="form.google_oauth_client_id"
                          type="text"
                          class="input font-mono text-sm"
                          placeholder="Google OAuth Client ID"
                        />
                      </div>
                      <div>
                        <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">Client Secret</label>
                        <input
                          v-model="form.google_oauth_client_secret"
                          type="password"
                          class="input font-mono text-sm"
                          :placeholder="
                            form.google_oauth_client_secret_configured
                              ? localText('密钥已配置，留空以保留当前值。', 'Secret configured. Leave empty to keep the current value.')
                              : 'Google OAuth Client Secret'
                          "
                        />
                      </div>
                    </div>

                    <div>
                      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                        {{ localText("后端回调地址", "Backend Callback URL") }}
                      </label>
                      <input
                        v-model="form.google_oauth_redirect_url"
                        type="url"
                        class="input font-mono text-sm"
                        placeholder="https://your-domain.com/api/v1/auth/oauth/google/callback"
                      />
                      <div class="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3">
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
                      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
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

          <!-- WeChat Connect OAuth 登录 -->
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

          <!-- DingTalk Connect OAuth 登录 -->
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

                  <!-- bypass_registration toggle（仅 internal_only 模式下可见可用） -->
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

                  <!-- 身份同步开关（仅 internal_only 模式下可见） -->
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

          <!-- Generic OIDC OAuth 登录 -->
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
        <!-- /Tab: Security — Registration, Turnstile, LinuxDo, OIDC -->

        <!-- Tab: Users -->
        <div v-show="activeTab === 'users'" class="space-y-6">
          <UsersPanel />
        </div>

        <div v-show="activeTab === 'general'" class="space-y-6">
          <GeneralPanel />
        </div>

	      <div v-show="activeTab === 'agreement'" class="space-y-6">
          <AgreementPanel />
        </div>

        <div v-show="activeTab === 'features'" class="space-y-6">
          <FeaturesPanel />
        </div>

        <div v-show="activeTab === 'payment'" class="space-y-6">
          <PaymentPanel />
        </div>

        <div v-show="activeTab === 'email'" class="space-y-6">
          <EmailPanel />
        </div>

      </form>

        <!-- Tab: Backup (must stay outside main form — backup UI may contain nested forms) -->
        <!-- Perf: v-if (not v-show) so BackupSettings only mounts — and only fires
             its s3-config/schedule/backups XHRs — when the Backup tab is opened,
             instead of on every Settings page load (default tab is 'general'). -->
        <div v-if="activeTab === 'backup'">
          <BackupSettings />
        </div>

        <!-- Save Button -->
        <div v-show="activeTab !== 'backup'" class="flex justify-end">
          <button
            type="button"
            :disabled="saving || loadFailed"
            class="btn btn-primary"
            @click="saveSettings"
          >
            <svg
              v-if="saving"
              class="h-4 w-4 animate-spin"
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
              />
              <path
                class="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              />
            </svg>
            {{ saving ? t("common.saving") : t("common.save") }}
          </button>
        </div>
    </div>
  </template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { adminAPI } from "@/api";
import {
  appendAuthSourceDefaultsToUpdateRequest,
  buildAuthSourceDefaultsState,
  normalizePlatformQuotasMap,
  sanitizePlatformQuotasMap,
  defaultWeChatConnectScopesForMode,
  deriveWeChatConnectStoredMode,
  normalizeDefaultSubscriptionSettings,
  resolveWeChatConnectModeCapabilities,
} from "@/api/admin/settings";
import type {
  AuthSourceDefaultsState,
  AuthSourceType,
  UpdateSettingsRequest,
  DefaultSubscriptionSetting,
  DefaultPlatformQuotasMap,
} from "@/api/admin/settings";
import type { AdminGroup, LoginAgreementDocument } from "@/types";
import Icon from "@/components/icons/Icon.vue";
import BackupSettings from "@/views/admin/BackupView.vue";
import { extractApiErrorMessage } from "@/utils/apiError";
import { useAppStore } from "@/stores";
import { useAdminSettingsStore } from "@/stores/adminSettings";
import {
  normalizeRegistrationEmailSuffixDomains,
} from "@/utils/registrationEmailPolicy";
import {
  provideSettingsState,
  type SettingsTab,
  type SettingsForm,
} from "@/composables/useSettingsState";

// ── Panel Components ──
import SecurityPanel from "./settings/SecurityPanel.vue";
import GatewayPanel from "./settings/GatewayPanel.vue";
import UsersPanel from "./settings/UsersPanel.vue";
import GeneralPanel from "./settings/GeneralPanel.vue";
import AgreementPanel from "./settings/AgreementPanel.vue";
import FeaturesPanel from "./settings/FeaturesPanel.vue";
import PaymentPanel from "./settings/PaymentPanel.vue";
import EmailPanel from "./settings/EmailPanel.vue";

const { t, locale } = useI18n();
const appStore = useAppStore();
const adminSettingsStore = useAdminSettingsStore();

const isZhLocale = computed(() => locale.value.startsWith("zh"));

function localText(zh: string, en: string): string {
  return isZhLocale.value ? zh : en;
}

// ── Tab Navigation ──

const activeTab = ref<SettingsTab>("general");
const settingsTabs = [
  { key: "general" as SettingsTab, icon: "home" as const },
  { key: "features" as SettingsTab, icon: "bolt" as const },
  { key: "security" as SettingsTab, icon: "shield" as const },
  { key: "users" as SettingsTab, icon: "user" as const },
  { key: "gateway" as SettingsTab, icon: "server" as const },
  { key: "payment" as SettingsTab, icon: "creditCard" as const },
  { key: "email" as SettingsTab, icon: "mail" as const },
  { key: "backup" as SettingsTab, icon: "database" as const },
];

const settingsTabKeyboardActions = {
  ArrowLeft: -1,
  ArrowUp: -1,
  ArrowRight: 1,
  ArrowDown: 1,
  Home: "first",
  End: "last",
} as const;

function selectSettingsTab(tab: SettingsTab): void {
  activeTab.value = tab;
}

function focusSettingsTab(tab: SettingsTab): void {
  window.requestAnimationFrame(() => {
    document.getElementById(`settings-tab-${tab}`)?.focus();
  });
}

function handleSettingsTabKeydown(event: KeyboardEvent, tab: SettingsTab): void {
  const action =
    settingsTabKeyboardActions[
      event.key as keyof typeof settingsTabKeyboardActions
    ];
  if (action === undefined) {
    return;
  }

  event.preventDefault();
  const currentIndex = settingsTabs.findIndex((item) => item.key === tab);
  let nextIndex = currentIndex < 0 ? 0 : currentIndex;

  if (action === "first") {
    nextIndex = 0;
  } else if (action === "last") {
    nextIndex = settingsTabs.length - 1;
  } else {
    nextIndex =
      (nextIndex + action + settingsTabs.length) % settingsTabs.length;
  }

  const nextTab = settingsTabs[nextIndex]?.key;
  if (!nextTab) {
    return;
  }

  selectSettingsTab(nextTab);
  focusSettingsTab(nextTab);
}

// ── Shared Settings State ──

const loading = ref(true);
const loadFailed = ref(false);
const saving = ref(false);
const smtpPasswordManuallyEdited = ref(false);
const registrationEmailSuffixWhitelistTags = ref<string[]>([]);
const registrationEmailSuffixWhitelistDraft = ref("");
const tablePageSizeOptionsInput = ref("10, 20, 50, 100");

const subscriptionGroups = ref<AdminGroup[]>([]);

// GatewayPanel template ref (for exposed state in save/load)
const gatewayPanelRef = ref<InstanceType<typeof GatewayPanel> | null>(null);

const tablePageSizeMin = 5;
const tablePageSizeMax = 1000;
const tablePageSizeDefault = 20;

// ── Login Agreement helpers (needed by saveSettings validation) ──

function normalizeLoginAgreementDocumentId(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/[-_]{2,}/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "");
}

function normalizeLoginAgreementDocumentsForSave(): LoginAgreementDocument[] {
  return form.login_agreement_documents
    .map((doc, index) => ({
      id:
        normalizeLoginAgreementDocumentId(doc.id || doc.title) ||
        `doc-${index + 1}`,
      title: doc.title.trim(),
      content_md: doc.content_md.trim(),
    }))
    .filter((doc) => doc.title || doc.content_md);
}

function findDuplicateLoginAgreementDocumentId(
  documents: LoginAgreementDocument[],
): string | null {
  const seen = new Set<string>();
  for (const doc of documents) {
    if (seen.has(doc.id)) {
      return doc.id;
    }
    seen.add(doc.id);
  }
  return null;
}

// Gateway panel-local state is now in GatewayPanel.vue;
// the parent accesses it via gatewayPanelRef.value.* (defineExpose)

// ── Table page size helpers (needed by saveSettings) ──

function parseTablePageSizeOptionsInput(raw: string): number[] | null {
  const tokens = raw
    .split(",")
    .map((token) => token.trim())
    .filter((token) => token.length > 0);

  if (tokens.length === 0) {
    return null;
  }

  const parsed = tokens.map((token) => Number(token));
  if (parsed.some((value) => !Number.isInteger(value))) {
    return null;
  }

  const deduped = Array.from(new Set(parsed)).sort((a, b) => a - b);
  if (
    deduped.some(
      (value) => value < tablePageSizeMin || value > tablePageSizeMax,
    )
  ) {
    return null;
  }

  return deduped;
}

function formatTablePageSizeOptions(options: number[]): string {
  return options.join(", ");
}

// Web Search config is now in GatewayPanel.vue

// ── Default subscription helpers ──

function findDuplicateDefaultSubscription(
  subscriptions: DefaultSubscriptionSetting[],
): DefaultSubscriptionSetting | undefined {
  const seenGroupIDs = new Set<number>();
  return subscriptions.find((item) => {
    if (seenGroupIDs.has(item.group_id)) {
      return true;
    }
    seenGroupIDs.add(item.group_id);
    return false;
  });
}

// ── Auth Source Defaults ──

const authSourceDefaults = reactive<AuthSourceDefaultsState>(
  buildAuthSourceDefaultsState({}),
);

const authSourceDefaultsMeta = computed(() => [
  {
    source: "email" as AuthSourceType,
    title: t("admin.settings.authSourceDefaults.sources.email.title"),
    description: t("admin.settings.authSourceDefaults.sources.email.description"),
  },
  {
    source: "linuxdo" as AuthSourceType,
    title: t("admin.settings.authSourceDefaults.sources.linuxdo.title"),
    description: t("admin.settings.authSourceDefaults.sources.linuxdo.description"),
  },
  {
    source: "oidc" as AuthSourceType,
    title: t("admin.settings.authSourceDefaults.sources.oidc.title"),
    description: t("admin.settings.authSourceDefaults.sources.oidc.description"),
  },
  {
    source: "wechat" as AuthSourceType,
    title: t("admin.settings.authSourceDefaults.sources.wechat.title"),
    description: t("admin.settings.authSourceDefaults.sources.wechat.description"),
  },
  {
    source: "github" as AuthSourceType,
    title: "GitHub",
    description: localText(
      "通过 GitHub 已验证邮箱首次注册或首次绑定时应用。",
      "Applied on first signup or first bind through a verified GitHub email.",
    ),
  },
  {
    source: "google" as AuthSourceType,
    title: "Google",
    description: localText(
      "通过 Google 已验证邮箱首次注册或首次绑定时应用。",
      "Applied on first signup or first bind through a verified Google email.",
    ),
  },
  {
    source: "dingtalk" as AuthSourceType,
    title: t("auth.dingtalkProviderName"),
    description: localText(
      "通过钉钉首次注册或首次绑定时应用。",
      "Applied on first signup or first bind through DingTalk.",
    ),
  },
]);

// ── Default subscription group options ──

const defaultSubscriptionGroupOptions = computed(() =>
  subscriptionGroups.value.map((group) => ({
    value: group.id,
    label: group.name,
    description: group.description,
    platform: group.platform,
    subscriptionType: group.subscription_type,
    rate: group.rate_multiplier,
  })),
);

// ── Callback URL helpers ──

const currentOrigin =
  typeof window !== "undefined" ? window.location.origin : "";

function buildApiCallbackUrl(path: string): string {
  const base = (form.api_base_url || currentOrigin).replace(/\/+$/, "");
  const apiRoot = base.endsWith("/api/v1") ? base : `${base}/api/v1`;
  return `${apiRoot}${path.startsWith("/") ? path : `/${path}`}`;
}

// ── Default Login Agreement Documents ──

function defaultLoginAgreementDocuments(): LoginAgreementDocument[] {
  return [
    {
      id: "terms",
      title: localText("服务条款", "Terms of Service"),
      content_md: "",
    },
    {
      id: "usage-policy",
      title: localText("使用政策", "Usage Policy"),
      content_md: "",
    },
    {
      id: "supported-regions",
      title: localText("支持的国家和地区", "Supported Countries and Regions"),
      content_md: "",
    },
    {
      id: "service-specific-terms",
      title: localText("服务特定条款", "Service-Specific Terms"),
      content_md: "",
    },
  ];
}

// ── Form State ──

const form = reactive<SettingsForm>({
  registration_enabled: true,
  email_verify_enabled: false,
  registration_email_suffix_whitelist: [],
  promo_code_enabled: true,
  invitation_code_enabled: false,
  password_reset_enabled: false,
  totp_enabled: false,
  totp_encryption_key_configured: false,
  login_agreement_enabled: false,
  login_agreement_mode: "modal",
  login_agreement_updated_at: "",
  login_agreement_documents: defaultLoginAgreementDocuments(),
  default_balance: 0,
  default_platform_quotas: normalizePlatformQuotasMap() as DefaultPlatformQuotasMap,
  affiliate_rebate_rate: 20,
  affiliate_rebate_freeze_hours: 0,
  affiliate_rebate_duration_days: 0,
  affiliate_rebate_per_invitee_cap: 0,
  affiliate_admin_recharge_enabled: false,
  default_concurrency: 1,
  default_subscriptions: [],
  site_name: 'TokenKey',
  site_logo: '',
  site_subtitle: 'AI API Gateway Platform',
  api_base_url: '',
  contact_info: '',
  doc_url: '',
  home_content: '',
  force_email_on_third_party_signup: false,
  default_user_rpm_limit: 0,
  backend_mode_enabled: false,
  hide_ccs_import_button: false,
  payment_enabled: false,
  risk_control_enabled: false,
  cyber_session_block_enabled: false,
  cyber_session_block_ttl_seconds: 3600,
  payment_min_amount: 1,
  payment_max_amount: 10000,
  payment_daily_limit: 50000,
  payment_max_pending_orders: 3,
  payment_order_timeout_minutes: 30,
  payment_balance_disabled: false,
  payment_balance_recharge_multiplier: 1,
  payment_subscription_usd_to_cny_rate: 0,
  payment_recharge_fee_rate: 0,
  payment_enabled_types: [],
  payment_help_image_url: "",
  payment_help_text: "",
  payment_product_name_prefix: "",
  payment_product_name_suffix: "",
  payment_load_balance_strategy: "round-robin",
  payment_cancel_rate_limit_enabled: false,
  payment_cancel_rate_limit_max: 10,
  payment_cancel_rate_limit_window: 1,
  payment_cancel_rate_limit_unit: "day",
  payment_cancel_rate_limit_window_mode: "rolling",
  payment_alipay_force_qrcode: false,
  table_default_page_size: tablePageSizeDefault,
  table_page_size_options: [10, 20, 50, 100],
  custom_menu_items: [],
  custom_endpoints: [],
  frontend_url: "",
  smtp_host: "",
  smtp_port: 587,
  smtp_username: "",
  smtp_password: "",
  smtp_password_configured: false,
  smtp_from_email: "",
  smtp_from_name: "",
  smtp_use_tls: true,
  turnstile_enabled: false,
  turnstile_site_key: "",
  turnstile_secret_key: "",
  turnstile_secret_key_configured: false,
  api_key_acl_trust_forwarded_ip: false,
  linuxdo_connect_enabled: false,
  linuxdo_connect_client_id: "",
  linuxdo_connect_client_secret: "",
  linuxdo_connect_client_secret_configured: false,
  linuxdo_connect_redirect_url: "",
  dingtalk_connect_enabled: false,
  dingtalk_connect_client_id: "",
  dingtalk_connect_client_secret: "",
  dingtalk_connect_client_secret_configured: false,
  dingtalk_connect_redirect_url: "",
  dingtalk_connect_corp_restriction_policy: "none",
  dingtalk_connect_internal_corp_id: "",
  dingtalk_connect_bypass_registration: false,
  dingtalk_connect_sync_corp_email: false,
  dingtalk_connect_sync_display_name: false,
  dingtalk_connect_sync_dept: false,
  dingtalk_connect_sync_corp_email_attr_key: "dingtalk_email",
  dingtalk_connect_sync_display_name_attr_key: "dingtalk_name",
  dingtalk_connect_sync_dept_attr_key: "dingtalk_department",
  dingtalk_connect_sync_corp_email_attr_name: localText("钉钉企业邮箱", "DingTalk Corporate Email"),
  dingtalk_connect_sync_display_name_attr_name: localText("钉钉姓名", "DingTalk Name"),
  dingtalk_connect_sync_dept_attr_name: localText("钉钉部门", "DingTalk Department"),
  wechat_connect_enabled: false,
  wechat_connect_app_id: "",
  wechat_connect_app_secret: "",
  wechat_connect_app_secret_configured: false,
  wechat_connect_open_app_id: "",
  wechat_connect_open_app_secret: "",
  wechat_connect_open_app_secret_configured: false,
  wechat_connect_mp_app_id: "",
  wechat_connect_mp_app_secret: "",
  wechat_connect_mp_app_secret_configured: false,
  wechat_connect_mobile_app_id: "",
  wechat_connect_mobile_app_secret: "",
  wechat_connect_mobile_app_secret_configured: false,
  wechat_connect_open_enabled: false,
  wechat_connect_mp_enabled: false,
  wechat_connect_mobile_enabled: false,
  wechat_connect_mode: "open",
  wechat_connect_scopes: "snsapi_login",
  wechat_connect_redirect_url: "",
  wechat_connect_frontend_redirect_url: "/auth/wechat/callback",
  oidc_connect_enabled: false,
  oidc_connect_provider_name: "OIDC",
  oidc_connect_client_id: "",
  oidc_connect_client_secret: "",
  oidc_connect_client_secret_configured: false,
  oidc_connect_issuer_url: "",
  oidc_connect_discovery_url: "",
  oidc_connect_authorize_url: "",
  oidc_connect_token_url: "",
  oidc_connect_userinfo_url: "",
  oidc_connect_jwks_url: "",
  oidc_connect_scopes: "openid email profile",
  oidc_connect_redirect_url: "",
  oidc_connect_frontend_redirect_url: "/auth/oidc/callback",
  oidc_connect_token_auth_method: "client_secret_post",
  oidc_connect_use_pkce: false,
  oidc_connect_validate_id_token: false,
  oidc_connect_allowed_signing_algs: "RS256,ES256,PS256",
  oidc_connect_clock_skew_seconds: 120,
  oidc_connect_require_email_verified: false,
  oidc_connect_userinfo_email_path: "",
  oidc_connect_userinfo_id_path: "",
  oidc_connect_userinfo_username_path: "",
  github_oauth_enabled: false,
  github_oauth_client_id: "",
  github_oauth_client_secret: "",
  github_oauth_client_secret_configured: false,
  github_oauth_redirect_url: "",
  github_oauth_frontend_redirect_url: "/auth/github/callback",
  google_oauth_enabled: false,
  google_oauth_client_id: "",
  google_oauth_client_secret: "",
  google_oauth_client_secret_configured: false,
  google_oauth_redirect_url: "",
  google_oauth_frontend_redirect_url: "/auth/google/callback",
  enable_model_fallback: false,
  fallback_model_anthropic: "claude-sonnet-4-6",
  fallback_model_openai: "gpt-4o",
  fallback_model_gemini: "gemini-2.5-pro",
  fallback_model_antigravity: "gemini-2.5-pro",
  enable_identity_patch: true,
  identity_patch_prompt: "",
  ops_monitoring_enabled: true,
  ops_realtime_monitoring_enabled: true,
  ops_query_mode_default: "auto",
  ops_metrics_interval_seconds: 60,
  min_claude_code_version: "",
  max_claude_code_version: "",
  allow_ungrouped_key_scheduling: false,
  openai_advanced_scheduler_enabled: false,
  enable_fingerprint_unification: true,
  enable_metadata_passthrough: false,
  enable_cch_signing: false,
  enable_claude_oauth_system_prompt_injection: true,
  claude_oauth_system_prompt: "",
  claude_oauth_system_prompt_blocks: "",
  enable_anthropic_cache_ttl_1h_injection: false,
  tk_anthropic_request_normalize_enabled: true,
  sticky_routing_enabled: true,
  rewrite_message_cache_control: false,
  enable_client_dateline_normalization: true,
  antigravity_user_agent_version: "",
  openai_codex_user_agent: "",
  min_codex_version: "",
  max_codex_version: "",
  codex_cli_only_blacklist: "",
  codex_cli_only_whitelist: "",
  codex_cli_only_allow_app_server_clients: false,
  codex_cli_only_engine_fingerprint_signals: "",
  balance_low_notify_enabled: false,
  balance_low_notify_threshold: 0,
  balance_low_notify_recharge_url: "",
  subscription_expiry_notify_enabled: true,
  account_quota_notify_enabled: false,
  account_quota_notify_emails: [],
  signup_bonus_enabled: false,
  signup_bonus_balance: 1.0,
  auto_generate_default_token: false,
  auto_generate_default_token_name: 'trial',
  pricing_catalog_public: false,
  channel_monitor_enabled: true,
  channel_monitor_default_interval_seconds: 60,
  available_channels_enabled: false,
  affiliate_enabled: false,
  allow_user_view_error_requests: false,
});

// ── Provide shared state to panel components ──

provideSettingsState({
  form,
  saving,
  loading,
  loadFailed,
  activeTab,
  localText,
  isZhLocale,
  currentOrigin,
  buildApiCallbackUrl,
  subscriptionGroups,
  defaultSubscriptionGroupOptions,
  authSourceDefaults,
  authSourceDefaultsMeta,
  registrationEmailSuffixWhitelistTags,
  saveSettings,
  loadSettings,
  loadSubscriptionGroups,
});

// ── Load Settings ──

async function loadSettings() {
  loading.value = true;
  loadFailed.value = false;
  try {
    const settings = await adminAPI.settings.getSettings();
    settings.payment_load_balance_strategy =
      settings.payment_load_balance_strategy || "round-robin";
    for (const [key, value] of Object.entries(settings)) {
      if (value !== null && value !== undefined) {
        (form as Record<string, unknown>)[key] = value;
      }
    }
    // Gateway panel-local state (Claude OAuth blocks, codex rows, fingerprint signals)
    // is hydrated automatically via watchers on form fields in GatewayPanel.vue.
    // Explicitly call hydrateFromForm() for synchronous initial hydration.
    gatewayPanelRef.value?.hydrateFromForm();
    form.login_agreement_mode =
      settings.login_agreement_mode === "checkbox" ? "checkbox" : "modal";
    form.login_agreement_updated_at =
      settings.login_agreement_updated_at || "2026-03-31";
    form.login_agreement_documents =
      Array.isArray(settings.login_agreement_documents) &&
      settings.login_agreement_documents.length > 0
        ? settings.login_agreement_documents.map((doc) => ({
            id: doc.id || "",
            title: doc.title || "",
            content_md: doc.content_md || "",
          }))
      : defaultLoginAgreementDocuments()
    registrationEmailSuffixWhitelistTags.value = normalizeRegistrationEmailSuffixDomains(
      settings.registration_email_suffix_whitelist
    )
    Object.assign(authSourceDefaults, buildAuthSourceDefaultsState(settings));
    form.default_platform_quotas = normalizePlatformQuotasMap(settings.default_platform_quotas);
    form.backend_mode_enabled = settings.backend_mode_enabled;
    form.default_subscriptions = normalizeDefaultSubscriptionSettings(
      settings.default_subscriptions,
    );
    registrationEmailSuffixWhitelistTags.value =
      normalizeRegistrationEmailSuffixDomains(
        settings.registration_email_suffix_whitelist,
      );
    tablePageSizeOptionsInput.value = formatTablePageSizeOptions(
      Array.isArray(settings.table_page_size_options)
        ? settings.table_page_size_options
        : [10, 20, 50, 100],
    );
    registrationEmailSuffixWhitelistDraft.value = "";
    form.smtp_password = "";
    smtpPasswordManuallyEdited.value = false;
    form.turnstile_secret_key = "";
    form.linuxdo_connect_client_secret = "";
    form.dingtalk_connect_client_secret = "";
    form.github_oauth_client_secret = "";
    form.google_oauth_client_secret = "";
    form.wechat_connect_app_secret = "";
    form.wechat_connect_open_app_secret = "";
    form.wechat_connect_mp_app_secret = "";
    form.wechat_connect_mobile_app_secret = "";
    const wechatCapabilities = resolveWeChatConnectModeCapabilities(
      settings.wechat_connect_open_enabled,
      settings.wechat_connect_mp_enabled,
      settings.wechat_connect_mobile_enabled,
      settings.wechat_connect_mode,
    );
    form.wechat_connect_open_enabled = wechatCapabilities.openEnabled;
    form.wechat_connect_mp_enabled = wechatCapabilities.mpEnabled;
    form.wechat_connect_mobile_enabled = wechatCapabilities.mobileEnabled;
    form.wechat_connect_mode = deriveWeChatConnectStoredMode(
      wechatCapabilities.openEnabled,
      wechatCapabilities.mpEnabled,
      wechatCapabilities.mobileEnabled,
      settings.wechat_connect_mode,
    );
    const legacyWeChatAppID = String(settings.wechat_connect_app_id || "").trim();
    const legacyWeChatSecretConfigured = Boolean(
      settings.wechat_connect_app_secret_configured,
    );
    if (!form.wechat_connect_open_app_id && wechatCapabilities.openEnabled) {
      form.wechat_connect_open_app_id = legacyWeChatAppID;
    }
    if (!form.wechat_connect_mp_app_id && wechatCapabilities.mpEnabled) {
      form.wechat_connect_mp_app_id = legacyWeChatAppID;
    }
    if (!form.wechat_connect_mobile_app_id && wechatCapabilities.mobileEnabled) {
      form.wechat_connect_mobile_app_id = legacyWeChatAppID;
    }
    if (
      !form.wechat_connect_open_app_secret_configured &&
      wechatCapabilities.openEnabled
    ) {
      form.wechat_connect_open_app_secret_configured =
        legacyWeChatSecretConfigured;
    }
    if (
      !form.wechat_connect_mp_app_secret_configured &&
      wechatCapabilities.mpEnabled
    ) {
      form.wechat_connect_mp_app_secret_configured = legacyWeChatSecretConfigured;
    }
    if (
      !form.wechat_connect_mobile_app_secret_configured &&
      wechatCapabilities.mobileEnabled
    ) {
      form.wechat_connect_mobile_app_secret_configured =
        legacyWeChatSecretConfigured;
    }
    form.wechat_connect_scopes = defaultWeChatConnectScopesForMode(
      form.wechat_connect_mode,
    );
    form.oidc_connect_client_secret = "";

    // OpenAI fast policy and web search are loaded/hydrated by GatewayPanel
    if (
      settings.openai_fast_policy_settings &&
      Array.isArray(settings.openai_fast_policy_settings.rules) &&
      gatewayPanelRef.value
    ) {
      gatewayPanelRef.value.openaiFastPolicyForm.rules =
        settings.openai_fast_policy_settings.rules.map((rule) => ({
          ...rule,
          user_ids: rule.user_ids ? [...rule.user_ids] : [],
          model_whitelist: rule.model_whitelist
            ? [...rule.model_whitelist]
            : [],
        }));
      gatewayPanelRef.value.openaiFastPolicyLoaded = true;
    }
  } catch (error: unknown) {
    loadFailed.value = true;
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.failedToLoad")),
    );
  } finally {
    loading.value = false;
  }
}

// ── Load Subscription Groups ──

async function loadSubscriptionGroups() {
  try {
    const groups = await adminAPI.groups.getAll();
    subscriptionGroups.value = groups.filter(
      (group) =>
        group.subscription_type === "subscription" && group.status === "active",
    );
  } catch (_error: unknown) {
    subscriptionGroups.value = [];
  }
}

// ── Save Settings ──

async function saveSettings() {
  saving.value = true;
  try {
    const normalizedTableDefaultPageSize = Math.floor(
      Number(form.table_default_page_size),
    );
    if (
      !Number.isInteger(normalizedTableDefaultPageSize) ||
      normalizedTableDefaultPageSize < tablePageSizeMin ||
      normalizedTableDefaultPageSize > tablePageSizeMax
    ) {
      appStore.showError(
        t("admin.settings.site.tableDefaultPageSizeRangeError", {
          min: tablePageSizeMin,
          max: tablePageSizeMax,
        }),
      );
      return;
    }

    const normalizedTablePageSizeOptions = parseTablePageSizeOptionsInput(
      tablePageSizeOptionsInput.value,
    );
    if (!normalizedTablePageSizeOptions) {
      appStore.showError(
        t("admin.settings.site.tablePageSizeOptionsFormatError", {
          min: tablePageSizeMin,
          max: tablePageSizeMax,
        }),
      );
      return;
    }

    form.table_default_page_size = normalizedTableDefaultPageSize;
    form.table_page_size_options = normalizedTablePageSizeOptions;

    const normalizedLoginAgreementDocuments =
      normalizeLoginAgreementDocumentsForSave();
    if (form.login_agreement_enabled && normalizedLoginAgreementDocuments.length === 0) {
      appStore.showError(
        localText(
          "启用登录条款确认时，至少需要保留一份文档。",
          "At least one document is required when login agreement is enabled.",
        ),
      );
      return;
    }
    const emptyTitleDocument = normalizedLoginAgreementDocuments.find(
      (doc) => !doc.title,
    );
    if (emptyTitleDocument) {
      appStore.showError(
        localText(
          "登录条款文档名称不能为空。",
          "Login agreement document title cannot be empty.",
        ),
      );
      return;
    }
    const duplicateLoginAgreementDocumentId =
      findDuplicateLoginAgreementDocumentId(normalizedLoginAgreementDocuments);
    if (duplicateLoginAgreementDocumentId) {
      appStore.showError(
        localText(
          `登录条款文档路由不能重复：/legal/${duplicateLoginAgreementDocumentId}`,
          `Login agreement document routes cannot be duplicated: /legal/${duplicateLoginAgreementDocumentId}`,
        ),
      );
      return;
    }
    form.login_agreement_mode =
      form.login_agreement_mode === "checkbox" ? "checkbox" : "modal";
    form.login_agreement_documents = normalizedLoginAgreementDocuments;

    const normalizedDefaultSubscriptions = normalizeDefaultSubscriptionSettings(
      form.default_subscriptions,
    );
    const duplicateDefaultSubscription = findDuplicateDefaultSubscription(
      normalizedDefaultSubscriptions,
    );
    if (duplicateDefaultSubscription) {
      appStore.showError(
        t("admin.settings.defaults.defaultSubscriptionsDuplicate", {
          groupId: duplicateDefaultSubscription.group_id,
        }),
      );
      return;
    }

    for (const authSource of authSourceDefaultsMeta.value) {
      authSourceDefaults[authSource.source].subscriptions =
        normalizeDefaultSubscriptionSettings(
          authSourceDefaults[authSource.source].subscriptions,
        );
      const duplicate = findDuplicateDefaultSubscription(
        authSourceDefaults[authSource.source].subscriptions,
      );
      if (duplicate) {
        appStore.showError(
          `${authSource.title}: ${t(
            "admin.settings.defaults.defaultSubscriptionsDuplicate",
            {
              groupId: duplicate.group_id,
            },
          )}`,
        );
        return;
      }
    }

    if (form.wechat_connect_mp_enabled && form.wechat_connect_mobile_enabled) {
      appStore.showError(
        localText(
          "公众号和移动应用不能同时启用。",
          "Official Account and Mobile App cannot be enabled at the same time.",
        ),
      );
      return;
    }

    const isValidHttpUrl = (url: string): boolean => {
      if (!url) return true;
      try {
        const u = new URL(url);
        return u.protocol === "http:" || u.protocol === "https:";
      } catch {
        return false;
      }
    };
    if (!isValidHttpUrl(form.frontend_url)) form.frontend_url = "";
    if (!isValidHttpUrl(form.doc_url)) form.doc_url = "";

    const wechatStoredMode = deriveWeChatConnectStoredMode(
      form.wechat_connect_open_enabled,
      form.wechat_connect_mp_enabled,
      form.wechat_connect_mobile_enabled,
      form.wechat_connect_mode,
    );
    // Serialize gateway panel state via ref
    const gw = gatewayPanelRef.value;
    const claudeOAuthSystemPromptBlocksJSON = gw
      ? gw.serializeClaudeOAuthSystemPromptBlocksToJSON(
          gw.claudeOAuthSystemPromptBlocks,
        )
      : form.claude_oauth_system_prompt_blocks || "";
    form.claude_oauth_system_prompt_blocks =
      claudeOAuthSystemPromptBlocksJSON;

    const payload: UpdateSettingsRequest = {
      registration_enabled: form.registration_enabled,
      email_verify_enabled: form.email_verify_enabled,
      registration_email_suffix_whitelist:
        registrationEmailSuffixWhitelistTags.value.map((suffix) =>
          suffix.startsWith("*.") ? suffix : `@${suffix}`,
        ),
      promo_code_enabled: form.promo_code_enabled,
      invitation_code_enabled: form.invitation_code_enabled,
      password_reset_enabled: form.password_reset_enabled,
      totp_enabled: form.totp_enabled,
      default_balance: form.default_balance,
      affiliate_rebate_rate: Math.min(
        100,
        Math.max(0, Number(form.affiliate_rebate_rate) || 0),
      ),
      affiliate_rebate_freeze_hours: Math.max(0, Math.min(720, Number(form.affiliate_rebate_freeze_hours) || 0)),
      affiliate_rebate_duration_days: Math.max(0, Math.min(3650, Math.floor(Number(form.affiliate_rebate_duration_days) || 0))),
      affiliate_rebate_per_invitee_cap: Math.max(0, Number(form.affiliate_rebate_per_invitee_cap) || 0),
      affiliate_admin_recharge_enabled: form.affiliate_admin_recharge_enabled,
      default_concurrency: form.default_concurrency,
      default_subscriptions: normalizedDefaultSubscriptions,
      force_email_on_third_party_signup: form.force_email_on_third_party_signup,
      default_user_rpm_limit: form.default_user_rpm_limit,
      site_name: form.site_name,
      site_logo: form.site_logo,
      site_subtitle: form.site_subtitle,
      api_base_url: form.api_base_url,
      contact_info: form.contact_info,
      doc_url: form.doc_url,
      home_content: form.home_content,
      hide_ccs_import_button: form.hide_ccs_import_button,
      table_default_page_size: form.table_default_page_size,
      table_page_size_options: form.table_page_size_options,
      custom_menu_items: form.custom_menu_items,
      custom_endpoints: form.custom_endpoints,
      frontend_url: form.frontend_url,
      smtp_host: form.smtp_host,
      smtp_port: form.smtp_port,
      smtp_username: form.smtp_username,
      smtp_password: form.smtp_password || undefined,
      smtp_from_email: form.smtp_from_email,
      smtp_from_name: form.smtp_from_name,
      smtp_use_tls: form.smtp_use_tls,
      turnstile_enabled: form.turnstile_enabled,
      turnstile_site_key: form.turnstile_site_key,
      turnstile_secret_key: form.turnstile_secret_key || undefined,
      api_key_acl_trust_forwarded_ip: form.api_key_acl_trust_forwarded_ip,
      linuxdo_connect_enabled: form.linuxdo_connect_enabled,
      linuxdo_connect_client_id: form.linuxdo_connect_client_id,
      linuxdo_connect_client_secret:
        form.linuxdo_connect_client_secret || undefined,
      linuxdo_connect_redirect_url: form.linuxdo_connect_redirect_url,
      dingtalk_connect_enabled: form.dingtalk_connect_enabled,
      dingtalk_connect_client_id: form.dingtalk_connect_client_id,
      dingtalk_connect_client_secret:
        form.dingtalk_connect_client_secret || undefined,
      dingtalk_connect_redirect_url: form.dingtalk_connect_redirect_url,
      dingtalk_connect_corp_restriction_policy:
        form.dingtalk_connect_corp_restriction_policy,
      dingtalk_connect_internal_corp_id: form.dingtalk_connect_internal_corp_id,
      dingtalk_connect_bypass_registration: form.dingtalk_connect_bypass_registration,
      dingtalk_connect_sync_corp_email: form.dingtalk_connect_sync_corp_email,
      dingtalk_connect_sync_display_name: form.dingtalk_connect_sync_display_name,
      dingtalk_connect_sync_dept: form.dingtalk_connect_sync_dept,
      dingtalk_connect_sync_corp_email_attr_key: form.dingtalk_connect_sync_corp_email_attr_key,
      dingtalk_connect_sync_display_name_attr_key: form.dingtalk_connect_sync_display_name_attr_key,
      dingtalk_connect_sync_dept_attr_key: form.dingtalk_connect_sync_dept_attr_key,
      dingtalk_connect_sync_corp_email_attr_name: form.dingtalk_connect_sync_corp_email_attr_name,
      dingtalk_connect_sync_display_name_attr_name: form.dingtalk_connect_sync_display_name_attr_name,
      dingtalk_connect_sync_dept_attr_name: form.dingtalk_connect_sync_dept_attr_name,
      wechat_connect_enabled: form.wechat_connect_enabled,
      wechat_connect_app_id:
        form.wechat_connect_open_app_id ||
        form.wechat_connect_mp_app_id ||
        form.wechat_connect_mobile_app_id ||
        form.wechat_connect_app_id,
      wechat_connect_app_secret: form.wechat_connect_app_secret || undefined,
      wechat_connect_open_app_id: form.wechat_connect_open_app_id,
      wechat_connect_open_app_secret:
        form.wechat_connect_open_app_secret || undefined,
      wechat_connect_mp_app_id: form.wechat_connect_mp_app_id,
      wechat_connect_mp_app_secret:
        form.wechat_connect_mp_app_secret || undefined,
      wechat_connect_mobile_app_id: form.wechat_connect_mobile_app_id,
      wechat_connect_mobile_app_secret:
        form.wechat_connect_mobile_app_secret || undefined,
      wechat_connect_open_enabled: form.wechat_connect_open_enabled,
      wechat_connect_mp_enabled: form.wechat_connect_mp_enabled,
      wechat_connect_mobile_enabled: form.wechat_connect_mobile_enabled,
      wechat_connect_mode: wechatStoredMode,
      wechat_connect_scopes:
        defaultWeChatConnectScopesForMode(wechatStoredMode),
      wechat_connect_redirect_url: form.wechat_connect_redirect_url,
      wechat_connect_frontend_redirect_url:
        form.wechat_connect_frontend_redirect_url,
      oidc_connect_enabled: form.oidc_connect_enabled,
      oidc_connect_provider_name: form.oidc_connect_provider_name,
      oidc_connect_client_id: form.oidc_connect_client_id,
      oidc_connect_client_secret: form.oidc_connect_client_secret || undefined,
      oidc_connect_issuer_url: form.oidc_connect_issuer_url,
      oidc_connect_discovery_url: form.oidc_connect_discovery_url,
      oidc_connect_authorize_url: form.oidc_connect_authorize_url,
      oidc_connect_token_url: form.oidc_connect_token_url,
      oidc_connect_userinfo_url: form.oidc_connect_userinfo_url,
      oidc_connect_jwks_url: form.oidc_connect_jwks_url,
      oidc_connect_scopes: form.oidc_connect_scopes,
      oidc_connect_redirect_url: form.oidc_connect_redirect_url,
      oidc_connect_frontend_redirect_url:
        form.oidc_connect_frontend_redirect_url,
      oidc_connect_token_auth_method: form.oidc_connect_token_auth_method,
      oidc_connect_use_pkce: form.oidc_connect_use_pkce,
      oidc_connect_validate_id_token: form.oidc_connect_validate_id_token,
      oidc_connect_allowed_signing_algs: form.oidc_connect_allowed_signing_algs,
      oidc_connect_clock_skew_seconds: form.oidc_connect_clock_skew_seconds,
      oidc_connect_require_email_verified:
        form.oidc_connect_require_email_verified,
      oidc_connect_userinfo_email_path: form.oidc_connect_userinfo_email_path,
      oidc_connect_userinfo_id_path: form.oidc_connect_userinfo_id_path,
      oidc_connect_userinfo_username_path:
        form.oidc_connect_userinfo_username_path,
      github_oauth_enabled: form.github_oauth_enabled,
      github_oauth_client_id: form.github_oauth_client_id,
      github_oauth_client_secret:
        form.github_oauth_client_secret || undefined,
      github_oauth_redirect_url: form.github_oauth_redirect_url,
      github_oauth_frontend_redirect_url:
        form.github_oauth_frontend_redirect_url,
      google_oauth_enabled: form.google_oauth_enabled,
      google_oauth_client_id: form.google_oauth_client_id,
      google_oauth_client_secret:
        form.google_oauth_client_secret || undefined,
      google_oauth_redirect_url: form.google_oauth_redirect_url,
      google_oauth_frontend_redirect_url:
        form.google_oauth_frontend_redirect_url,
      enable_model_fallback: form.enable_model_fallback,
      fallback_model_anthropic: form.fallback_model_anthropic,
      fallback_model_openai: form.fallback_model_openai,
      fallback_model_gemini: form.fallback_model_gemini,
      fallback_model_antigravity: form.fallback_model_antigravity,
      enable_identity_patch: form.enable_identity_patch,
      identity_patch_prompt: form.identity_patch_prompt,
      min_claude_code_version: form.min_claude_code_version,
      max_claude_code_version: form.max_claude_code_version,
      allow_ungrouped_key_scheduling: form.allow_ungrouped_key_scheduling,
      enable_fingerprint_unification: form.enable_fingerprint_unification,
      enable_metadata_passthrough: form.enable_metadata_passthrough,
      enable_cch_signing: form.enable_cch_signing,
      enable_claude_oauth_system_prompt_injection:
        form.enable_claude_oauth_system_prompt_injection,
      claude_oauth_system_prompt: form.claude_oauth_system_prompt?.trim()
        ? form.claude_oauth_system_prompt
        : "",
      claude_oauth_system_prompt_blocks: claudeOAuthSystemPromptBlocksJSON,
      enable_anthropic_cache_ttl_1h_injection:
        form.enable_anthropic_cache_ttl_1h_injection,
      sticky_routing_enabled: form.sticky_routing_enabled,
      rewrite_message_cache_control: form.rewrite_message_cache_control,
      tk_anthropic_request_normalize_enabled:
        form.tk_anthropic_request_normalize_enabled,
      enable_client_dateline_normalization:
        form.enable_client_dateline_normalization,
      antigravity_user_agent_version:
        form.antigravity_user_agent_version?.trim() || "",
      openai_codex_user_agent:
        form.openai_codex_user_agent?.trim() || "",
      min_codex_version: form.min_codex_version?.trim() || "",
      max_codex_version: form.max_codex_version?.trim() || "",
      codex_cli_only_allow_app_server_clients:
        form.codex_cli_only_allow_app_server_clients,
      codex_cli_only_engine_fingerprint_signals: gw
        ? gw.serializeFingerprintRowsToJSON(gw.codexFingerprintRows)
        : form.codex_cli_only_engine_fingerprint_signals || "",
      codex_cli_only_blacklist: gw
        ? gw.serializeCodexRowsToJSON(gw.codexBlacklistRows)
        : form.codex_cli_only_blacklist || "",
      codex_cli_only_whitelist: gw
        ? gw.serializeCodexRowsToJSON(gw.codexWhitelistRows)
        : form.codex_cli_only_whitelist || "",
      payment_enabled: form.payment_enabled,
      risk_control_enabled: form.risk_control_enabled,
      cyber_session_block_enabled: form.cyber_session_block_enabled,
      cyber_session_block_ttl_seconds:
        Number(form.cyber_session_block_ttl_seconds) || 3600,
      payment_min_amount: Number(form.payment_min_amount) || 0,
      payment_max_amount: Number(form.payment_max_amount) || 0,
      payment_daily_limit: Number(form.payment_daily_limit) || 0,
      payment_max_pending_orders: Number(form.payment_max_pending_orders) || 0,
      payment_order_timeout_minutes:
        Number(form.payment_order_timeout_minutes) || 0,
      payment_balance_disabled: form.payment_balance_disabled,
      payment_balance_recharge_multiplier:
        Number(form.payment_balance_recharge_multiplier) || 1,
      payment_subscription_usd_to_cny_rate:
        Number(form.payment_subscription_usd_to_cny_rate) || 0,
      payment_recharge_fee_rate: Number(form.payment_recharge_fee_rate) || 0,
      payment_enabled_types: form.payment_enabled_types,
      payment_load_balance_strategy: form.payment_load_balance_strategy,
      payment_product_name_prefix: form.payment_product_name_prefix,
      payment_product_name_suffix: form.payment_product_name_suffix,
      payment_help_image_url: form.payment_help_image_url,
      payment_help_text: form.payment_help_text,
      payment_cancel_rate_limit_enabled: form.payment_cancel_rate_limit_enabled,
      payment_cancel_rate_limit_max:
        Number(form.payment_cancel_rate_limit_max) || 10,
      payment_cancel_rate_limit_window:
        Number(form.payment_cancel_rate_limit_window) || 1,
      payment_cancel_rate_limit_unit: form.payment_cancel_rate_limit_unit,
      payment_cancel_rate_limit_window_mode:
        form.payment_cancel_rate_limit_window_mode,
      payment_alipay_force_qrcode: form.payment_alipay_force_qrcode,
      openai_low_upstream_rate_priority_enabled:
        form.openai_low_upstream_rate_priority_enabled,
      openai_oauth_scheduling_rate_multiplier:
        form.openai_oauth_scheduling_rate_multiplier,
      openai_advanced_scheduler_enabled: form.openai_advanced_scheduler_enabled,
      balance_low_notify_enabled: form.balance_low_notify_enabled,
      balance_low_notify_threshold:
        Number(form.balance_low_notify_threshold) || 0,
      balance_low_notify_recharge_url: (form.balance_low_notify_recharge_url =
        form.balance_low_notify_recharge_url || currentOrigin),
      subscription_expiry_notify_enabled:
        form.subscription_expiry_notify_enabled,
      account_quota_notify_enabled: form.account_quota_notify_enabled,
      account_quota_notify_emails: (form.account_quota_notify_emails || []).filter((e) => e.email.trim() !== ''),
      signup_bonus_enabled: form.signup_bonus_enabled,
      signup_bonus_balance: Math.max(0, Number(form.signup_bonus_balance) || 0),
      auto_generate_default_token: form.auto_generate_default_token,
      auto_generate_default_token_name:
        (form.auto_generate_default_token_name || '').trim() || 'trial',
      pricing_catalog_public: form.pricing_catalog_public,
      channel_monitor_enabled: form.channel_monitor_enabled,
      channel_monitor_default_interval_seconds:
        Number(form.channel_monitor_default_interval_seconds) || 60,
      available_channels_enabled: form.available_channels_enabled,
      affiliate_enabled: form.affiliate_enabled,
      allow_user_view_error_requests: form.allow_user_view_error_requests,
    };

    if (gw?.openaiFastPolicyLoaded) {
      payload.openai_fast_policy_settings = {
        rules: gw.openaiFastPolicyForm.rules.map((rule) => {
          const whitelist = (rule.model_whitelist || [])
            .map((p: string) => p.trim())
            .filter((p: string) => p !== "");
          const hasWhitelist = whitelist.length > 0;
          return {
            service_tier: rule.service_tier,
            action: rule.action,
            scope: rule.scope,
            user_ids:
              rule.user_ids && rule.user_ids.length > 0
                ? [...rule.user_ids]
                : undefined,
            error_message:
              rule.action === "block" ? rule.error_message : undefined,
            model_whitelist: hasWhitelist ? whitelist : undefined,
            fallback_action: hasWhitelist
              ? rule.fallback_action || "pass"
              : undefined,
            fallback_error_message:
              hasWhitelist && rule.fallback_action === "block"
                ? rule.fallback_error_message
                : undefined,
          };
        }),
      };
    }

    payload.default_platform_quotas = sanitizePlatformQuotasMap(form.default_platform_quotas);
    appendAuthSourceDefaultsToUpdateRequest(payload, authSourceDefaults);

    const updated = await adminAPI.settings.updateSettings(payload);
    for (const [key, value] of Object.entries(updated)) {
      if (key === "openai_fast_policy_settings") continue;
      if (value !== null && value !== undefined) {
        (form as Record<string, unknown>)[key] = value;
      }
    }
    Object.assign(authSourceDefaults, buildAuthSourceDefaultsState(updated));
    form.default_platform_quotas = normalizePlatformQuotasMap(updated.default_platform_quotas);
    registrationEmailSuffixWhitelistTags.value =
      normalizeRegistrationEmailSuffixDomains(
        updated.registration_email_suffix_whitelist,
      );
    tablePageSizeOptionsInput.value = formatTablePageSizeOptions(
      Array.isArray(updated.table_page_size_options)
        ? updated.table_page_size_options
        : [10, 20, 50, 100],
    );
    registrationEmailSuffixWhitelistDraft.value = "";
    form.smtp_password = "";
    smtpPasswordManuallyEdited.value = false;
    form.turnstile_secret_key = "";
    form.linuxdo_connect_client_secret = "";
    form.dingtalk_connect_client_secret = "";
    form.github_oauth_client_secret = "";
    form.google_oauth_client_secret = "";
    form.wechat_connect_app_secret = "";
    form.wechat_connect_open_app_secret = "";
    form.wechat_connect_mp_app_secret = "";
    form.wechat_connect_mobile_app_secret = "";
    const updatedWechatCapabilities = resolveWeChatConnectModeCapabilities(
      updated.wechat_connect_open_enabled,
      updated.wechat_connect_mp_enabled,
      updated.wechat_connect_mobile_enabled,
      updated.wechat_connect_mode,
    );
    form.wechat_connect_open_enabled = updatedWechatCapabilities.openEnabled;
    form.wechat_connect_mp_enabled = updatedWechatCapabilities.mpEnabled;
    form.wechat_connect_mobile_enabled =
      updatedWechatCapabilities.mobileEnabled;
    form.wechat_connect_mode = deriveWeChatConnectStoredMode(
      updatedWechatCapabilities.openEnabled,
      updatedWechatCapabilities.mpEnabled,
      updatedWechatCapabilities.mobileEnabled,
      updated.wechat_connect_mode,
    );
    form.wechat_connect_scopes = defaultWeChatConnectScopesForMode(
      form.wechat_connect_mode,
    );
    form.oidc_connect_client_secret = "";
    if (
      updated.openai_fast_policy_settings &&
      Array.isArray(updated.openai_fast_policy_settings.rules) &&
      gw
    ) {
      gw.openaiFastPolicyForm.rules =
        updated.openai_fast_policy_settings.rules.map((rule) => ({
          ...rule,
          user_ids: rule.user_ids ? [...rule.user_ids] : [],
          model_whitelist: rule.model_whitelist
            ? [...rule.model_whitelist]
            : [],
        }));
      gw.openaiFastPolicyLoaded = true;
    }
    const wsOk = gw ? await gw.saveWebSearchConfig() : true;
    await appStore.fetchPublicSettings(true);
    await adminSettingsStore.fetch(true);
    if (wsOk) {
      appStore.showSuccess(t("admin.settings.settingsSaved"));
    }
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.failedToSave")),
    );
  } finally {
    saving.value = false;
  }
}
// ── Lifecycle ──

onMounted(() => {
  loadSettings();
  loadSubscriptionGroups();
});
</script>

<style scoped>
.default-sub-group-select :deep(.select-trigger) {
  @apply h-[42px];
}

.default-sub-delete-btn {
  @apply h-[42px];
}

/* ============ 系统设置 Tab 导航 ============ */
.settings-tabs-shell {
  @apply sticky z-20 -mx-1 rounded-2xl border border-white/80 bg-white/90 p-1.5 backdrop-blur-xl;
  top: 4.75rem;
  box-shadow:
    0 12px 28px rgb(15 23 42 / 0.07),
    0 1px 0 rgb(255 255 255 / 0.9) inset;
}

.settings-tabs-scroll {
  @apply overflow-x-auto;
  -ms-overflow-style: none;
  scrollbar-width: none;
}

.settings-tabs-scroll::-webkit-scrollbar {
  display: none;
}

.settings-tabs {
  @apply flex min-w-max items-center gap-1;
}

.settings-tab {
  @apply relative isolate flex h-10 min-w-[6.75rem] shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-xl border border-transparent px-3 text-sm font-medium text-gray-600 outline-none transition-colors duration-200 ease-out dark:text-gray-300;
}

@media (min-width: 768px) {
  .settings-tabs {
    @apply min-w-full;
  }

  .settings-tab {
    @apply min-w-0 flex-1 basis-0 overflow-hidden px-2 text-[13px];
  }

  .settings-tab-icon {
    @apply h-6 w-6;
  }
}

.settings-tab::before {
  @apply absolute inset-0 -z-10 rounded-xl opacity-0 transition-opacity duration-200;
  content: "";
  background: linear-gradient(135deg, rgb(248 250 252 / 0.95), rgb(241 245 249 / 0.8));
}

.settings-tab:hover::before,
.settings-tab:focus-visible::before {
  opacity: 1;
}

.settings-tab:focus-visible {
  @apply ring-2 ring-primary-500/40 ring-offset-2 ring-offset-white dark:ring-offset-dark-900;
}

.settings-tab-active {
  @apply border-primary-200/80 bg-white text-primary-700 shadow-sm dark:border-primary-400/30 dark:bg-dark-700/95 dark:text-primary-200;
  box-shadow:
    0 8px 18px rgb(15 23 42 / 0.08),
    0 1px 0 rgb(255 255 255 / 0.92) inset;
}

.settings-tab-active::before {
  opacity: 0;
}

.settings-tab-active::after {
  position: absolute;
  right: 0.75rem;
  bottom: 0.25rem;
  left: 0.75rem;
  height: 2px;
  border-radius: 9999px;
  content: "";
  background: linear-gradient(90deg, #14b8a6, #0ea5e9);
}

.settings-tab-icon {
  @apply flex h-7 w-7 shrink-0 items-center justify-center rounded-lg text-gray-500 transition-colors duration-200 dark:text-gray-400;
}

.settings-tab:hover .settings-tab-icon,
.settings-tab:focus-visible .settings-tab-icon {
  @apply text-gray-700 dark:text-gray-200;
}

.settings-tab-active .settings-tab-icon {
  @apply bg-primary-50 text-primary-600 dark:bg-primary-400/10 dark:text-primary-300;
}

.settings-tab-label {
  @apply min-w-0 overflow-hidden text-ellipsis whitespace-nowrap leading-none;
}
</style>

<style>
/* Dark-mode overrides for the settings tabs shell. Kept in an UNSCOPED block
   because Vue's scoped-CSS compiler was dropping the `:global(.dark) ...`
   rules in the production build, leaving inactive tabs unreadable on dark. */
.dark .settings-tabs-shell {
  border-color: rgb(51 65 85 / 0.65);
  background: rgb(15 23 42 / 0.86);
  box-shadow:
    0 16px 36px rgb(0 0 0 / 0.28),
    0 1px 0 rgb(255 255 255 / 0.06) inset;
}

.dark .settings-tab::before {
  background: linear-gradient(135deg, rgb(30 41 59 / 0.9), rgb(51 65 85 / 0.62));
}

.dark .settings-tab-active {
  box-shadow:
    0 12px 26px rgb(0 0 0 / 0.22),
    0 1px 0 rgb(255 255 255 / 0.08) inset;
}
</style>
