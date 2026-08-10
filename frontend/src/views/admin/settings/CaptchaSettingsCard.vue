<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useSettingsState } from "@/composables/useSettingsState";
import Toggle from "@/components/common/Toggle.vue";

const { t } = useI18n();
const { form } = useSettingsState();

// 天御中国站与国际站是两套独立账号体系，控制台与文档入口不通用
const tencentCaptchaLinks = computed(() =>
  form.tencent_captcha_region === "intl"
    ? {
        console: "https://console.tencentcloud.com/captcha/graphical",
        cloudKeys: "https://console.tencentcloud.com/cam/capi",
        webDocs: "https://www.tencentcloud.com/document/product/1159/49680",
      }
    : {
        console: "https://console.cloud.tencent.com/captcha",
        cloudKeys: "https://console.cloud.tencent.com/cam/capi",
        webDocs: "https://cloud.tencent.com/document/product/1110/36841",
      },
);

type CaptchaProvider = "turnstile" | "tencent" | "aliyun";

const selectedProvider = ref<CaptchaProvider>("turnstile");

function syncSelectedProvider(): void {
  if (form.tencent_captcha_enabled) {
    selectedProvider.value = "tencent";
  } else if (form.aliyun_captcha_enabled) {
    selectedProvider.value = "aliyun";
  } else if (form.turnstile_enabled) {
    selectedProvider.value = "turnstile";
  }
}

function applyProvider(provider: CaptchaProvider | null): void {
  form.turnstile_enabled = provider === "turnstile";
  form.tencent_captcha_enabled = provider === "tencent";
  form.aliyun_captcha_enabled = provider === "aliyun";
}

const enabled = computed({
  get: () =>
    form.turnstile_enabled ||
    form.tencent_captcha_enabled ||
    form.aliyun_captcha_enabled,
  set: (value: boolean) => applyProvider(value ? selectedProvider.value : null),
});

function selectProvider(provider: CaptchaProvider): void {
  selectedProvider.value = provider;
  applyProvider(provider);
}

watch(
  () => [
    form.turnstile_enabled,
    form.tencent_captcha_enabled,
    form.aliyun_captcha_enabled,
  ],
  syncSelectedProvider,
  { immediate: true },
);
</script>

<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t("admin.settings.captcha.title") }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t("admin.settings.captcha.description") }}
      </p>
    </div>

    <div class="space-y-5 p-6">
      <div class="flex items-center justify-between gap-4">
        <div>
          <label class="font-medium text-gray-900 dark:text-white">
            {{ t("admin.settings.captcha.enable") }}
          </label>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.captcha.enableHint") }}
          </p>
        </div>
        <Toggle v-model="enabled" data-testid="captcha-enabled-toggle" />
      </div>

      <template v-if="enabled">
        <div class="border-t border-gray-100 pt-4 dark:border-dark-700">
          <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.captcha.provider") }}
          </div>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="provider in (['turnstile', 'tencent', 'aliyun'] as CaptchaProvider[])"
              :key="provider"
              type="button"
              :data-testid="`captcha-provider-${provider}`"
              class="btn btn-secondary"
              :class="{ 'border-primary-500 text-primary-600': selectedProvider === provider }"
              @click="selectProvider(provider)"
            >
              {{ t(`admin.settings.captcha.provider${provider.charAt(0).toUpperCase()}${provider.slice(1)}`) }}
            </button>
          </div>
        </div>

        <div v-if="selectedProvider === 'turnstile'" class="grid grid-cols-1 gap-5 border-t border-gray-100 pt-4 dark:border-dark-700">
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.turnstile.siteKey") }}</span>
            <input v-model="form.turnstile_site_key" type="text" class="input font-mono text-sm" />
          </label>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.turnstile.secretKey") }}</span>
            <input v-model="form.turnstile_secret_key" type="password" class="input font-mono text-sm" />
            <span class="block text-xs font-normal text-gray-500 dark:text-gray-400">
              {{ form.turnstile_secret_key_configured ? t("admin.settings.turnstile.secretKeyConfiguredHint") : t("admin.settings.turnstile.secretKeyHint") }}
            </span>
          </label>
        </div>

        <div v-else-if="selectedProvider === 'tencent'" class="grid grid-cols-1 gap-5 border-t border-gray-100 pt-4 dark:border-dark-700">
          <div>
            <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t("admin.settings.tencentCaptcha.region") }}</div>
            <div class="flex gap-2">
              <button
                type="button"
                data-testid="tencent-captcha-region-cn"
                class="btn btn-secondary"
                :class="{ 'border-primary-500 text-primary-600': form.tencent_captcha_region !== 'intl' }"
                @click="form.tencent_captcha_region = 'cn'"
              >{{ t("admin.settings.tencentCaptcha.regionCn") }}</button>
              <button
                type="button"
                data-testid="tencent-captcha-region-intl"
                class="btn btn-secondary"
                :class="{ 'border-primary-500 text-primary-600': form.tencent_captcha_region === 'intl' }"
                @click="form.tencent_captcha_region = 'intl'"
              >{{ t("admin.settings.tencentCaptcha.regionIntl") }}</button>
            </div>
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">{{ t("admin.settings.tencentCaptcha.regionHint") }}</p>
          </div>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.tencentCaptcha.appId") }}</span>
            <input v-model="form.tencent_captcha_app_id" type="text" class="input font-mono text-sm" />
          </label>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.tencentCaptcha.appSecretKey") }}</span>
            <input v-model="form.tencent_captcha_app_secret_key" type="password" class="input font-mono text-sm" />
          </label>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.tencentCaptcha.cloudSecretId") }}</span>
            <input v-model="form.tencent_captcha_cloud_secret_id" type="password" class="input font-mono text-sm" />
          </label>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.tencentCaptcha.cloudSecretKey") }}</span>
            <input v-model="form.tencent_captcha_cloud_secret_key" type="password" class="input font-mono text-sm" />
          </label>
          <div class="flex flex-wrap gap-3 text-sm">
            <a :href="tencentCaptchaLinks.console" target="_blank" rel="noopener noreferrer" class="text-primary-600 hover:text-primary-500">{{ t("admin.settings.tencentCaptcha.openCaptchaConsole") }}</a>
            <a :href="tencentCaptchaLinks.cloudKeys" target="_blank" rel="noopener noreferrer" class="text-primary-600 hover:text-primary-500">{{ t("admin.settings.tencentCaptcha.createCloudKeys") }}</a>
            <a :href="tencentCaptchaLinks.webDocs" target="_blank" rel="noopener noreferrer" class="text-primary-600 hover:text-primary-500">{{ t("admin.settings.tencentCaptcha.openWebDocs") }}</a>
          </div>
        </div>

        <div v-else class="grid grid-cols-1 gap-5 border-t border-gray-100 pt-4 dark:border-dark-700">
          <div>
            <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t("admin.settings.aliyunCaptcha.region") }}</div>
            <div class="flex gap-2">
              <button type="button" class="btn btn-secondary" @click="form.aliyun_captcha_region = 'cn'">{{ t("admin.settings.aliyunCaptcha.regionCn") }}</button>
              <button type="button" class="btn btn-secondary" @click="form.aliyun_captcha_region = 'sgp'">{{ t("admin.settings.aliyunCaptcha.regionSgp") }}</button>
            </div>
          </div>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.aliyunCaptcha.prefix") }}</span>
            <input v-model="form.aliyun_captcha_prefix" type="text" class="input font-mono text-sm" />
          </label>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.aliyunCaptcha.sceneId") }}</span>
            <input v-model="form.aliyun_captcha_scene_id" type="text" class="input font-mono text-sm" />
          </label>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.aliyunCaptcha.accessKeyId") }}</span>
            <input v-model="form.aliyun_captcha_access_key_id" type="text" class="input font-mono text-sm" />
          </label>
          <label class="space-y-2 text-sm font-medium text-gray-700 dark:text-gray-300">
            <span>{{ t("admin.settings.aliyunCaptcha.accessKeySecret") }}</span>
            <input v-model="form.aliyun_captcha_access_key_secret" type="password" class="input font-mono text-sm" />
          </label>
        </div>
      </template>
    </div>
  </div>
</template>
