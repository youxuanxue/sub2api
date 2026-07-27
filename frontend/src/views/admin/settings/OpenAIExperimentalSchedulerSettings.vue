<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import Toggle from "@/components/common/Toggle.vue";
import { useSettingsState } from "@/composables/useSettingsState";

const { t } = useI18n();
const { form } = useSettingsState();

type OverrideKey =
  | "openai_advanced_scheduler_lb_top_k"
  | "openai_advanced_scheduler_weight_priority"
  | "openai_advanced_scheduler_weight_load"
  | "openai_advanced_scheduler_weight_queue"
  | "openai_advanced_scheduler_weight_error_rate"
  | "openai_advanced_scheduler_weight_ttft"
  | "openai_advanced_scheduler_weight_reset"
  | "openai_advanced_scheduler_weight_quota_headroom"
  | "openai_advanced_scheduler_weight_upstream_cost"
  | "openai_advanced_scheduler_weight_previous_response"
  | "openai_advanced_scheduler_weight_session_sticky";

type EffectiveKey =
  | "openai_advanced_scheduler_effective_lb_top_k"
  | "openai_advanced_scheduler_effective_weight_priority"
  | "openai_advanced_scheduler_effective_weight_load"
  | "openai_advanced_scheduler_effective_weight_queue"
  | "openai_advanced_scheduler_effective_weight_error_rate"
  | "openai_advanced_scheduler_effective_weight_ttft"
  | "openai_advanced_scheduler_effective_weight_reset"
  | "openai_advanced_scheduler_effective_weight_quota_headroom"
  | "openai_advanced_scheduler_effective_weight_upstream_cost"
  | "openai_advanced_scheduler_effective_weight_previous_response"
  | "openai_advanced_scheduler_effective_weight_session_sticky";

const weightFields = computed<
  Array<{ key: OverrideKey; label: string; placeholder: string }>
>(() => {
  const placeholder = (effectiveKey: EffectiveKey, fallbackValue: string) => {
    const effectiveValue = String(
      (form as Record<string, unknown>)[effectiveKey] ?? "",
    ).trim();
    return t("admin.settings.openaiExperimentalScheduler.defaultPlaceholder", {
      value: effectiveValue || fallbackValue,
    });
  };

  return [
    {
      key: "openai_advanced_scheduler_lb_top_k",
      label: t("admin.settings.openaiExperimentalScheduler.topKLabel"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_lb_top_k",
        "7",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_priority",
      label: t("admin.settings.openaiExperimentalScheduler.priorityWeight"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_priority",
        "1",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_load",
      label: t("admin.settings.openaiExperimentalScheduler.loadWeight"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_load",
        "1",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_queue",
      label: t("admin.settings.openaiExperimentalScheduler.queueWeight"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_queue",
        "0.7",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_error_rate",
      label: t("admin.settings.openaiExperimentalScheduler.errorRateWeight"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_error_rate",
        "0.8",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_ttft",
      label: t("admin.settings.openaiExperimentalScheduler.ttftWeight"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_ttft",
        "0.5",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_reset",
      label: t("admin.settings.openaiExperimentalScheduler.resetWeight"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_reset",
        "0",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_quota_headroom",
      label: t(
        "admin.settings.openaiExperimentalScheduler.quotaHeadroomWeight",
      ),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_quota_headroom",
        "0",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_upstream_cost",
      label: t("admin.settings.openaiExperimentalScheduler.upstreamCostWeight"),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_upstream_cost",
        "0",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_previous_response",
      label: t(
        "admin.settings.openaiExperimentalScheduler.previousResponseWeight",
      ),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_previous_response",
        "5",
      ),
    },
    {
      key: "openai_advanced_scheduler_weight_session_sticky",
      label: t(
        "admin.settings.openaiExperimentalScheduler.sessionStickyWeight",
      ),
      placeholder: placeholder(
        "openai_advanced_scheduler_effective_weight_session_sticky",
        "3",
      ),
    },
  ];
});
</script>

<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t("admin.settings.scheduling.title") }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t("admin.settings.scheduling.description") }}
      </p>
    </div>
    <div class="space-y-5 p-6">
      <div class="flex items-center justify-between">
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.scheduling.allowUngroupedKey") }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.scheduling.allowUngroupedKeyHint") }}
          </p>
        </div>
        <Toggle v-model="form.allow_ungrouped_key_scheduling" />
      </div>

      <div
        v-if="!form.openai_advanced_scheduler_enabled"
        class="flex items-center justify-between border-t border-gray-100 pt-5 dark:border-dark-700"
      >
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.lowRatePriorityTitle",
              )
            }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.lowRatePriorityDescription",
              )
            }}
          </p>
        </div>
        <Toggle
          v-model="form.openai_low_upstream_rate_priority_enabled"
          data-testid="openai-low-rate-priority-toggle"
        />
      </div>

      <div
        v-if="
          !form.openai_advanced_scheduler_enabled &&
          form.openai_low_upstream_rate_priority_enabled
        "
        class="flex flex-col items-stretch gap-3 border-t border-gray-100 pt-5 sm:flex-row sm:items-start sm:justify-between sm:gap-6 dark:border-dark-700"
      >
        <div class="min-w-0">
          <label
            class="text-sm font-medium text-gray-700 dark:text-gray-300"
            for="openai-oauth-scheduling-rate-multiplier"
          >
            {{ t("admin.settings.openaiExperimentalScheduler.oauthRateTitle") }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.oauthRatePriorityDescription",
              )
            }}
          </p>
        </div>
        <div class="relative w-full shrink-0 sm:w-32">
          <input
            id="openai-oauth-scheduling-rate-multiplier"
            v-model.number="form.openai_oauth_scheduling_rate_multiplier"
            class="input pr-8"
            data-testid="openai-oauth-scheduling-rate-multiplier"
            min="0"
            required
            step="0.01"
            type="number"
          />
          <span
            class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-sm text-gray-400"
            >x</span
          >
        </div>
      </div>

      <div
        class="flex items-center justify-between border-t border-gray-100 pt-5 dark:border-dark-700"
      >
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.openaiExperimentalScheduler.title") }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.openaiExperimentalScheduler.description") }}
          </p>
        </div>
        <Toggle
          v-model="form.openai_advanced_scheduler_enabled"
          data-testid="openai-advanced-scheduler-toggle"
        />
      </div>

      <div
        v-if="form.openai_advanced_scheduler_enabled"
        class="flex items-center justify-between border-t border-gray-100 pt-5 dark:border-dark-700"
      >
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.stickyWeightedTitle",
              )
            }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.stickyWeightedDescription",
              )
            }}
          </p>
        </div>
        <Toggle
          v-model="form.openai_advanced_scheduler_sticky_weighted_enabled"
        />
      </div>

      <div
        v-if="form.openai_advanced_scheduler_enabled"
        class="flex items-center justify-between border-t border-gray-100 pt-5 dark:border-dark-700"
      >
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.subscriptionPriorityTitle",
              )
            }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.subscriptionPriorityDescription",
              )
            }}
          </p>
        </div>
        <Toggle
          v-model="form.openai_advanced_scheduler_subscription_priority_enabled"
        />
      </div>

      <div
        v-if="form.openai_advanced_scheduler_enabled"
        class="flex flex-col items-stretch gap-3 border-t border-gray-100 pt-5 sm:flex-row sm:items-start sm:justify-between sm:gap-6 dark:border-dark-700"
      >
        <div class="min-w-0">
          <label
            class="text-sm font-medium text-gray-700 dark:text-gray-300"
            for="openai-oauth-scheduling-rate-multiplier"
          >
            {{ t("admin.settings.openaiExperimentalScheduler.oauthRateTitle") }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.openaiExperimentalScheduler.oauthRateWeightedDescription",
              )
            }}
          </p>
        </div>
        <div class="relative w-full shrink-0 sm:w-32">
          <input
            id="openai-oauth-scheduling-rate-multiplier"
            v-model.number="form.openai_oauth_scheduling_rate_multiplier"
            class="input pr-8"
            data-testid="openai-oauth-scheduling-rate-multiplier"
            min="0"
            required
            step="0.01"
            type="number"
          />
          <span
            class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-sm text-gray-400"
            >x</span
          >
        </div>
      </div>

      <div
        v-if="form.openai_advanced_scheduler_enabled"
        class="border-t border-gray-100 pt-5 dark:border-dark-700"
        data-testid="openai-advanced-scheduler-weights"
      >
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.settings.openaiExperimentalScheduler.weightsTitle") }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t("admin.settings.openaiExperimentalScheduler.weightsDescription")
            }}
          </p>
        </div>

        <div
          class="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-5"
        >
          <label
            v-for="field in weightFields"
            :key="field.key"
            class="block"
          >
            <span class="text-xs font-medium text-gray-600 dark:text-gray-400">
              {{ field.label }}
            </span>
            <input
              v-model="form[field.key]"
              class="input mt-1"
              inputmode="decimal"
              :placeholder="field.placeholder"
              type="text"
            />
          </label>
        </div>
      </div>
    </div>
  </div>
</template>
