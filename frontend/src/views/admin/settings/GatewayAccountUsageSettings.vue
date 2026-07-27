<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminAPI } from "@/api";
import Toggle from "@/components/common/Toggle.vue";
import { useAppStore } from "@/stores";
import { extractApiErrorMessage } from "@/utils/apiError";

const { t } = useI18n();
const appStore = useAppStore();

const upstreamBillingProbeLoading = ref(true);
const upstreamBillingProbeSaving = ref(false);
const upstreamBillingProbeForm = reactive({
  enabled: true,
  interval_minutes: 30,
});

async function loadUpstreamBillingProbeSettings() {
  upstreamBillingProbeLoading.value = true;
  try {
    Object.assign(
      upstreamBillingProbeForm,
      await adminAPI.accounts.getUpstreamBillingProbeSettings(),
    );
  } catch (_error: unknown) {
    // Keep defaults when this optional setting cannot be loaded.
  } finally {
    upstreamBillingProbeLoading.value = false;
  }
}

async function saveUpstreamBillingProbeSettings() {
  upstreamBillingProbeSaving.value = true;
  try {
    const updated = await adminAPI.accounts.updateUpstreamBillingProbeSettings({
      ...upstreamBillingProbeForm,
    });
    Object.assign(upstreamBillingProbeForm, updated);
    appStore.showSuccess(t("admin.settings.upstreamBillingProbe.saved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(
        error,
        t("admin.settings.upstreamBillingProbe.saveFailed"),
      ),
    );
  } finally {
    upstreamBillingProbeSaving.value = false;
  }
}

const ollamaCloudUsageLoading = ref(true);
const ollamaCloudUsageSaving = ref(false);
const ollamaCloudUsageForm = reactive({
  enabled: false,
  interval_minutes: 60,
  debounce_minutes: 1,
});

async function loadOllamaCloudUsageSettings() {
  ollamaCloudUsageLoading.value = true;
  try {
    Object.assign(
      ollamaCloudUsageForm,
      await adminAPI.accounts.getOllamaCloudUsageSettings(),
    );
  } catch (_error: unknown) {
    // Keep the fail-safe disabled defaults when this optional setting cannot be loaded.
  } finally {
    ollamaCloudUsageLoading.value = false;
  }
}

async function saveOllamaCloudUsageSettings() {
  ollamaCloudUsageSaving.value = true;
  try {
    const updated = await adminAPI.accounts.updateOllamaCloudUsageSettings({
      ...ollamaCloudUsageForm,
    });
    Object.assign(ollamaCloudUsageForm, updated);
    appStore.showSuccess(t("admin.settings.ollamaCloudUsage.saved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(
        error,
        t("admin.settings.ollamaCloudUsage.saveFailed"),
      ),
    );
  } finally {
    ollamaCloudUsageSaving.value = false;
  }
}

onMounted(() => {
  loadUpstreamBillingProbeSettings();
  loadOllamaCloudUsageSettings();
});
</script>

<template>
  <div class="space-y-6">
    <div class="card" data-testid="upstream-billing-probe-settings">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.upstreamBillingProbe.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.upstreamBillingProbe.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div
          v-if="upstreamBillingProbeLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <template v-else>
          <div class="flex items-center justify-between gap-4">
            <div>
              <label class="font-medium text-gray-900 dark:text-white">
                {{ t("admin.settings.upstreamBillingProbe.enabled") }}
              </label>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.upstreamBillingProbe.enabledHint") }}
              </p>
            </div>
            <Toggle
              v-model="upstreamBillingProbeForm.enabled"
              :aria-label="t('admin.settings.upstreamBillingProbe.enabled')"
              data-testid="upstream-billing-probe-enabled"
            />
          </div>

          <div
            v-if="upstreamBillingProbeForm.enabled"
            class="border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <label
              class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              for="upstream-billing-probe-interval"
            >
              {{ t("admin.settings.upstreamBillingProbe.intervalMinutes") }}
            </label>
            <input
              id="upstream-billing-probe-interval"
              v-model.number="upstreamBillingProbeForm.interval_minutes"
              type="number"
              min="5"
              max="1440"
              class="input w-32"
              data-testid="upstream-billing-probe-interval"
              @keydown.enter.prevent="saveUpstreamBillingProbeSettings"
            />
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.upstreamBillingProbe.intervalHint") }}
            </p>
          </div>

          <div
            class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="upstreamBillingProbeSaving"
              data-testid="upstream-billing-probe-save"
              @click="saveUpstreamBillingProbeSettings"
            >
              {{
                upstreamBillingProbeSaving
                  ? t("common.saving")
                  : t("common.save")
              }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <div class="card" data-testid="ollama-cloud-usage-global-settings">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.ollamaCloudUsage.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.ollamaCloudUsage.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div
          v-if="ollamaCloudUsageLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <template v-else>
          <div class="flex items-center justify-between gap-4">
            <div>
              <label class="font-medium text-gray-900 dark:text-white">
                {{ t("admin.settings.ollamaCloudUsage.enabled") }}
              </label>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.ollamaCloudUsage.enabledHint") }}
              </p>
            </div>
            <Toggle
              v-model="ollamaCloudUsageForm.enabled"
              :aria-label="t('admin.settings.ollamaCloudUsage.enabled')"
              data-testid="ollama-cloud-usage-global-enabled"
            />
          </div>

          <div
            v-if="ollamaCloudUsageForm.enabled"
            class="space-y-4 border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                for="ollama-cloud-usage-debounce"
              >
                {{ t("admin.settings.ollamaCloudUsage.debounceMinutes") }}
              </label>
              <input
                id="ollama-cloud-usage-debounce"
                v-model.number="ollamaCloudUsageForm.debounce_minutes"
                type="number"
                min="1"
                max="60"
                class="input w-32"
                data-testid="ollama-cloud-usage-global-debounce"
                @keydown.enter.prevent="saveOllamaCloudUsageSettings"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.ollamaCloudUsage.debounceHint") }}
              </p>
            </div>
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
                for="ollama-cloud-usage-interval"
              >
                {{ t("admin.settings.ollamaCloudUsage.intervalMinutes") }}
              </label>
              <input
                id="ollama-cloud-usage-interval"
                v-model.number="ollamaCloudUsageForm.interval_minutes"
                type="number"
                min="15"
                max="1440"
                class="input w-32"
                data-testid="ollama-cloud-usage-global-interval"
                @keydown.enter.prevent="saveOllamaCloudUsageSettings"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.ollamaCloudUsage.intervalHint") }}
              </p>
            </div>
          </div>

          <div
            class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="ollamaCloudUsageSaving"
              data-testid="ollama-cloud-usage-global-save"
              @click="saveOllamaCloudUsageSettings"
            >
              {{
                ollamaCloudUsageSaving ? t("common.saving") : t("common.save")
              }}
            </button>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>
