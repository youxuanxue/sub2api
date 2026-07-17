<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { useSettingsState } from "@/composables/useSettingsState";
import { adminAPI } from "@/api";
import { useAppStore } from "@/stores";
import { extractI18nErrorMessage } from "@/utils/apiError";
import { normalizeVisibleMethod } from "@/components/payment/paymentFlow";
import type { ProviderInstance } from "@/types/payment";

import Toggle from "@/components/common/Toggle.vue";
import Select from "@/components/common/Select.vue";
import ImageUpload from "@/components/common/ImageUpload.vue";
import PaymentProviderList from "@/components/payment/PaymentProviderList.vue";
import PaymentProviderDialog from "@/components/payment/PaymentProviderDialog.vue";
import ConfirmDialog from "@/components/common/ConfirmDialog.vue";

const { form, saveSettings } = useSettingsState();
const { t, locale } = useI18n();
const appStore = useAppStore();

// ── Computed hrefs ──

const paymentGuideHref = computed(() =>
  locale.value.startsWith("zh")
    ? "https://github.com/youxuanxue/sub2api/blob/main/docs/PAYMENT_CN.md"
    : "https://github.com/youxuanxue/sub2api/blob/main/docs/PAYMENT.md",
);

const paymentMethodsHref = computed(() =>
  locale.value.startsWith("zh")
    ? "https://github.com/youxuanxue/sub2api/blob/main/docs/PAYMENT_CN.md#支持的支付方式"
    : "https://github.com/youxuanxue/sub2api/blob/main/docs/PAYMENT.md#supported-payment-methods",
);

// ── Payment types ──

const allPaymentTypes = computed(() => [
  { value: "easypay", label: t("payment.methods.easypay") },
  { value: "alipay", label: t("payment.methods.alipay") },
  { value: "wxpay", label: t("payment.methods.wxpay") },
  { value: "stripe", label: t("payment.methods.stripe") },
  { value: "airwallex", label: t("payment.methods.airwallex") },
]);

function isPaymentTypeEnabled(type: string): boolean {
  return form.payment_enabled_types.includes(type);
}

const hasAnyPaymentTypeEnabled = computed(
  () => form.payment_enabled_types.length > 0,
);

function togglePaymentType(type: string) {
  if (form.payment_enabled_types.includes(type)) {
    form.payment_enabled_types = form.payment_enabled_types.filter(
      (t) => t !== type,
    );
    // Disable all provider instances matching this type
    disableProvidersByType(type);
  } else {
    form.payment_enabled_types = [...form.payment_enabled_types, type];
  }
}

async function disableProvidersByType(type: string) {
  const matching = providers.value.filter(
    (p) => p.provider_key === type && p.enabled,
  );
  for (const p of matching) {
    try {
      await adminAPI.payment.updateProvider(p.id, { enabled: false });
      p.enabled = false;
    } catch (err: unknown) {
      slog("disable provider failed", p.id, err);
    }
  }
}

// ── Provider management state ──

function slog(...args: unknown[]) {
  console.warn("[payment]", ...args);
}

const providersLoading = ref(false);
const providerSaving = ref(false);
const providers = ref<ProviderInstance[]>([]);
const showProviderDialog = ref(false);
const showDeleteProviderDialog = ref(false);
const editingProvider = ref<ProviderInstance | null>(null);
const deletingProviderId = ref<number | null>(null);
const providerDialogRef = ref<InstanceType<
  typeof PaymentProviderDialog
> | null>(null);

const providerKeyOptions = computed(() => [
  { value: "easypay", label: t("admin.settings.payment.providerEasypay") },
  { value: "alipay", label: t("admin.settings.payment.providerAlipay") },
  { value: "wxpay", label: t("admin.settings.payment.providerWxpay") },
  { value: "stripe", label: t("admin.settings.payment.providerStripe") },
  { value: "airwallex", label: t("admin.settings.payment.providerAirwallex") },
]);

const enabledProviderKeyOptions = computed(() => {
  const enabled = form.payment_enabled_types;
  return providerKeyOptions.value.filter((opt) => enabled.includes(opt.value));
});

const loadBalanceOptions = computed(() => [
  {
    value: "round-robin",
    label: t("admin.settings.payment.strategyRoundRobin"),
  },
  {
    value: "least-amount",
    label: t("admin.settings.payment.strategyLeastAmount"),
  },
]);

const cancelRateLimitUnitOptions = computed(() => [
  {
    value: "minute",
    label: t("admin.settings.payment.cancelRateLimitUnitMinute"),
  },
  { value: "hour", label: t("admin.settings.payment.cancelRateLimitUnitHour") },
  { value: "day", label: t("admin.settings.payment.cancelRateLimitUnitDay") },
]);

const cancelRateLimitModeOptions = computed(() => [
  {
    value: "rolling",
    label: t("admin.settings.payment.cancelRateLimitWindowModeRolling"),
  },
  {
    value: "fixed",
    label: t("admin.settings.payment.cancelRateLimitWindowModeFixed"),
  },
]);

// ── Provider enablement conflict detection ──

type ProviderEnablementCandidate = Pick<
  ProviderInstance,
  "id" | "provider_key" | "supported_types" | "enabled" | "name"
>;

function getProviderVisibleMethods(
  provider: ProviderEnablementCandidate,
): Array<"alipay" | "wxpay"> {
  if (!provider.enabled) {
    return [];
  }

  const supportedTypes = Array.isArray(provider.supported_types)
    ? provider.supported_types
    : [];
  const methods = new Set<"alipay" | "wxpay">();
  const addMethod = (type: string) => {
    const method = normalizeVisibleMethod(type);
    if (method === "alipay" || method === "wxpay") {
      methods.add(method);
    }
  };

  if (provider.provider_key === "alipay") {
    if (supportedTypes.length === 0) {
      methods.add("alipay");
    } else {
      supportedTypes.forEach((type) => {
        if (normalizeVisibleMethod(type) === "alipay") {
          methods.add("alipay");
        }
      });
    }
  } else if (provider.provider_key === "wxpay") {
    if (supportedTypes.length === 0) {
      methods.add("wxpay");
    } else {
      supportedTypes.forEach((type) => {
        if (normalizeVisibleMethod(type) === "wxpay") {
          methods.add("wxpay");
        }
      });
    }
  } else if (provider.provider_key === "easypay") {
    supportedTypes.forEach(addMethod);
  }

  return Array.from(methods);
}

function findProviderEnablementConflict(
  candidate: ProviderEnablementCandidate,
): { method: "alipay" | "wxpay"; conflicting: ProviderInstance } | null {
  const claimedMethods = getProviderVisibleMethods(candidate);
  if (claimedMethods.length === 0) {
    return null;
  }

  for (const other of providers.value) {
    if (other.id === candidate.id || !other.enabled) {
      continue;
    }

    const otherMethods = getProviderVisibleMethods(other);
    const matchedMethod = claimedMethods.find((method) =>
      otherMethods.includes(method),
    );
    if (matchedMethod) {
      return {
        method: matchedMethod,
        conflicting: other,
      };
    }
  }

  return null;
}

function showProviderEnablementConflict(
  conflict: { method: "alipay" | "wxpay"; conflicting: ProviderInstance },
) {
  appStore.showError(
    t("admin.settings.payment.enableConflict", {
      method: t(`payment.methods.${conflict.method}`),
      provider: conflict.conflicting.name,
    }),
  );
}

// ── Provider CRUD ──

async function loadProviders() {
  providersLoading.value = true;
  try {
    const res = await adminAPI.payment.getProviders();
    // Normalize supported_types: backend returns null when the list is empty
    // (Go nil slice -> JSON null). Without this, ProviderCard's isSelected()
    // throws TypeError on null.includes(), causing the card to vanish.
    providers.value = (res.data || []).map((p) => ({
      ...p,
      supported_types: Array.isArray(p.supported_types)
        ? p.supported_types
        : [],
    }));
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, "payment.errors", t("common.error")));
  } finally {
    providersLoading.value = false;
  }
}

function openCreateProvider() {
  editingProvider.value = null;
  providerDialogRef.value?.reset(
    enabledProviderKeyOptions.value[0]?.value || "easypay",
  );
  showProviderDialog.value = true;
}

function openEditProvider(provider: ProviderInstance) {
  editingProvider.value = provider;
  providerDialogRef.value?.loadProvider(provider);
  showProviderDialog.value = true;
}

async function handleSaveProvider(payload: Partial<ProviderInstance>) {
  providerSaving.value = true;
  try {
    const candidate: ProviderEnablementCandidate = {
      id: editingProvider.value?.id ?? 0,
      provider_key:
        payload.provider_key ?? editingProvider.value?.provider_key ?? "",
      supported_types:
        payload.supported_types ?? editingProvider.value?.supported_types ?? [],
      enabled: payload.enabled ?? editingProvider.value?.enabled ?? false,
      name: payload.name ?? editingProvider.value?.name ?? "",
    };
    const conflict = findProviderEnablementConflict(candidate);
    if (conflict) {
      showProviderEnablementConflict(conflict);
      return;
    }

    if (editingProvider.value) {
      await adminAPI.payment.updateProvider(editingProvider.value.id, payload);
    } else {
      await adminAPI.payment.createProvider(payload);
    }
    showProviderDialog.value = false;
    // Reload full list (API returns decrypted/formatted data with correct sort order)
    await loadProviders();
    // Auto-save settings so provider changes take effect immediately
    await saveSettings();
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, "payment.errors", t("common.error")));
  } finally {
    providerSaving.value = false;
  }
}

async function handleToggleField(
  provider: ProviderInstance,
  field: "enabled" | "refund_enabled" | "allow_user_refund",
) {
  let newValue: boolean;
  if (field === "enabled") newValue = !provider.enabled;
  else if (field === "refund_enabled") newValue = !provider.refund_enabled;
  else newValue = !provider.allow_user_refund;

  if (field === "enabled" && newValue) {
    const conflict = findProviderEnablementConflict({
      id: provider.id,
      provider_key: provider.provider_key,
      supported_types: provider.supported_types,
      enabled: true,
      name: provider.name,
    });
    if (conflict) {
      showProviderEnablementConflict(conflict);
      return;
    }
  }

  const payload: Record<string, boolean> = { [field]: newValue };
  // Cascade: turning off refund_enabled also turns off allow_user_refund
  if (field === "refund_enabled" && !newValue) {
    payload.allow_user_refund = false;
  }
  try {
    await adminAPI.payment.updateProvider(provider.id, payload);
    await loadProviders();
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, "payment.errors", t("common.error")));
  }
}

async function handleToggleType(provider: ProviderInstance, type: string) {
  const currentTypes = Array.isArray(provider.supported_types)
    ? provider.supported_types
    : [];
  const updated = currentTypes.includes(type)
    ? currentTypes.filter((t) => t !== type)
    : [...currentTypes, type];
  const conflict = findProviderEnablementConflict({
    id: provider.id,
    provider_key: provider.provider_key,
    supported_types: updated,
    enabled: provider.enabled,
    name: provider.name,
  });
  if (conflict) {
    showProviderEnablementConflict(conflict);
    return;
  }
  try {
    await adminAPI.payment.updateProvider(provider.id, {
      supported_types: updated,
    } as any);
    await loadProviders();
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, "payment.errors", t("common.error")));
  }
}

function confirmDeleteProvider(provider: ProviderInstance) {
  deletingProviderId.value = provider.id;
  showDeleteProviderDialog.value = true;
}

async function handleReorderProviders(
  updates: { id: number; sort_order: number }[],
) {
  try {
    await Promise.all(
      updates.map((u) =>
        adminAPI.payment.updateProvider(u.id, {
          sort_order: u.sort_order,
        } as Partial<ProviderInstance>),
      ),
    );
    await loadProviders();
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, "payment.errors", t("common.error")));
    loadProviders();
  }
}

async function handleDeleteProvider() {
  if (!deletingProviderId.value) return;
  try {
    await adminAPI.payment.deleteProvider(deletingProviderId.value);
    appStore.showSuccess(t("common.deleted"));
    showDeleteProviderDialog.value = false;
    loadProviders();
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, "payment.errors", t("common.error")));
  }
}

// ── Init ──

onMounted(() => {
  loadProviders();
});
</script>

<template>
  <div class="space-y-6">
    <!-- Payment System Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.payment.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.payment.description") }}
          <a
            :href="paymentGuideHref"
            target="_blank"
            rel="noopener noreferrer"
            class="ml-2 inline-flex items-center text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
          >
            <svg
              class="mr-0.5 h-3.5 w-3.5"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
              />
            </svg>
            {{ t("admin.settings.payment.configGuide") }}
          </a>
        </p>
      </div>
      <div class="space-y-4 p-6">
        <!-- Enable toggle -->
        <div class="flex items-center justify-between">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{
              t("admin.settings.payment.enabled")
            }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.payment.enabledHint") }}
            </p>
          </div>
          <Toggle v-model="form.payment_enabled" />
        </div>
        <template v-if="form.payment_enabled">
          <!-- Row 1: Product name -->
          <div class="grid grid-cols-3 gap-3">
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.productNamePrefix")
              }}</label
              ><input
                v-model="form.payment_product_name_prefix"
                type="text"
                class="input"
                placeholder="TokenKey"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.productNameSuffix")
              }}</label
              ><input
                v-model="form.payment_product_name_suffix"
                type="text"
                class="input"
                placeholder="CNY"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.preview")
              }}</label>
              <div
                class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-sm text-gray-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300"
              >
                {{
                  (form.payment_product_name_prefix || "TokenKey") +
                  " 100 " +
                  (form.payment_product_name_suffix || "CNY")
                }}
              </div>
            </div>
          </div>
          <!-- Row 2: Balance toggle + amounts -->
          <div class="grid grid-cols-2 gap-3 sm:grid-cols-5">
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.minAmount")
              }}</label
              ><input
                :value="form.payment_min_amount || ''"
                @input="
                  form.payment_min_amount =
                    parseFloat(
                      ($event.target as HTMLInputElement).value,
                    ) || 0
                "
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.settings.payment.noLimit')"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.maxAmount")
              }}</label
              ><input
                :value="form.payment_max_amount || ''"
                @input="
                  form.payment_max_amount =
                    parseFloat(
                      ($event.target as HTMLInputElement).value,
                    ) || 0
                "
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.settings.payment.noLimit')"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.dailyLimit")
              }}</label
              ><input
                :value="form.payment_daily_limit || ''"
                @input="
                  form.payment_daily_limit =
                    parseFloat(
                      ($event.target as HTMLInputElement).value,
                    ) || 0
                "
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.settings.payment.noLimit')"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.balanceRechargeMultiplier")
              }}</label>
              <input
                :value="form.payment_balance_recharge_multiplier || ''"
                @input="
                  form.payment_balance_recharge_multiplier =
                    parseFloat(
                      ($event.target as HTMLInputElement).value,
                    ) || 1
                "
                type="number"
                step="0.01"
                min="0.01"
                class="input"
              />
              <p class="mt-0.5 text-xs text-gray-400">
                {{
                  t(
                    "admin.settings.payment.balanceRechargeMultiplierHint",
                  )
                }}
              </p>
              <p
                class="mt-1 text-xs font-medium text-primary-600 dark:text-primary-400"
              >
                {{
                  t("admin.settings.payment.balanceRechargePreview", {
                    usd: (
                      Number(form.payment_balance_recharge_multiplier) ||
                      1
                    ).toFixed(2),
                  })
                }}
              </p>
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.rechargeFeeRate")
              }}</label>
              <div class="relative">
                <input
                  :value="form.payment_recharge_fee_rate ?? ''"
                  @input="
                    form.payment_recharge_fee_rate = Math.min(
                      100,
                      Math.max(
                        0,
                        Math.round(
                          parseFloat(
                            ($event.target as HTMLInputElement).value ||
                              '0',
                          ) * 100,
                        ) / 100,
                      ),
                    )
                  "
                  type="number"
                  step="0.01"
                  min="0"
                  max="100"
                  class="input pr-8"
                />
                <span
                  class="pointer-events-none absolute inset-y-0 right-0 flex items-center pr-3 text-gray-400"
                  >%</span
                >
              </div>
              <p class="mt-0.5 text-xs text-gray-400">
                {{ t("admin.settings.payment.rechargeFeeRateHint") }}
              </p>
              <p
                v-if="(Number(form.payment_recharge_fee_rate) || 0) > 0"
                class="mt-1 text-xs font-medium text-primary-600 dark:text-primary-400"
              >
                {{
                  t("admin.settings.payment.rechargeFeePreview", {
                    fee: (
                      Number(form.payment_recharge_fee_rate) || 0
                    ).toFixed(2),
                  })
                }}
              </p>
            </div>
            <div>
              <label class="input-label"
                >{{ t("admin.settings.payment.orderTimeout") }}
                <span class="text-red-500">*</span></label
              ><input
                v-model.number="form.payment_order_timeout_minutes"
                type="number"
                min="1"
                class="input"
                required
              />
              <p class="mt-0.5 text-xs text-gray-400">
                {{ t("admin.settings.payment.orderTimeoutHint") }}
              </p>
            </div>
          </div>
          <!-- Row 3: Pending orders + load balance + cancel rate limit (all in one row) -->
          <div class="flex flex-wrap items-end gap-4">
            <div class="w-28">
              <label class="input-label">{{
                t("admin.settings.payment.maxPendingOrders")
              }}</label
              ><input
                v-model.number="form.payment_max_pending_orders"
                type="number"
                min="1"
                class="input"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.loadBalanceStrategy")
              }}</label>
              <Select
                v-model="form.payment_load_balance_strategy"
                :options="loadBalanceOptions"
                class="w-40"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.cancelRateLimit")
              }}</label>
              <div class="flex items-center gap-2">
                <button
                  type="button"
                  :class="[
                    'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
                    form.payment_cancel_rate_limit_enabled
                      ? 'bg-primary-500'
                      : 'bg-gray-300 dark:bg-dark-600',
                  ]"
                  @click="
                    form.payment_cancel_rate_limit_enabled =
                      !form.payment_cancel_rate_limit_enabled
                  "
                >
                  <span
                    :class="[
                      'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                      form.payment_cancel_rate_limit_enabled
                        ? 'translate-x-5'
                        : 'translate-x-0',
                    ]"
                  />
                </button>
                <Select
                  v-model="form.payment_cancel_rate_limit_window_mode"
                  :options="cancelRateLimitModeOptions"
                  class="w-24"
                  :disabled="!form.payment_cancel_rate_limit_enabled"
                />
                <span
                  :class="[
                    'text-sm whitespace-nowrap',
                    form.payment_cancel_rate_limit_enabled
                      ? 'text-gray-700 dark:text-gray-300'
                      : 'text-gray-400 dark:text-gray-600',
                  ]"
                  >{{
                    t("admin.settings.payment.cancelRateLimitEvery")
                  }}</span
                >
                <input
                  v-model.number="form.payment_cancel_rate_limit_window"
                  type="number"
                  min="1"
                  required
                  class="input w-14 text-center"
                  :disabled="!form.payment_cancel_rate_limit_enabled"
                />
                <Select
                  v-model="form.payment_cancel_rate_limit_unit"
                  :options="cancelRateLimitUnitOptions"
                  class="w-28"
                  :disabled="!form.payment_cancel_rate_limit_enabled"
                />
                <span
                  :class="[
                    'text-sm whitespace-nowrap',
                    form.payment_cancel_rate_limit_enabled
                      ? 'text-gray-700 dark:text-gray-300'
                      : 'text-gray-400 dark:text-gray-600',
                  ]"
                  >{{
                    t("admin.settings.payment.cancelRateLimitAllowMax")
                  }}</span
                >
                <input
                  v-model.number="form.payment_cancel_rate_limit_max"
                  type="number"
                  min="1"
                  required
                  class="input w-14 text-center"
                  :disabled="!form.payment_cancel_rate_limit_enabled"
                />
                <span
                  :class="[
                    'text-sm whitespace-nowrap',
                    form.payment_cancel_rate_limit_enabled
                      ? 'text-gray-700 dark:text-gray-300'
                      : 'text-gray-400 dark:text-gray-600',
                  ]"
                  >{{
                    t("admin.settings.payment.cancelRateLimitTimes")
                  }}</span
                >
              </div>
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.alipayForceQRCode")
              }}</label>
              <div class="flex items-center gap-2">
                <button
                  type="button"
                  :class="[
                    'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
                    form.payment_alipay_force_qrcode
                      ? 'bg-primary-500'
                      : 'bg-gray-300 dark:bg-dark-600',
                  ]"
                  @click="
                    form.payment_alipay_force_qrcode =
                      !form.payment_alipay_force_qrcode
                  "
                >
                  <span
                    :class="[
                      'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                      form.payment_alipay_force_qrcode
                        ? 'translate-x-5'
                        : 'translate-x-0',
                    ]"
                  />
                </button>
                <span class="text-sm text-gray-500 dark:text-gray-400">{{
                  t("admin.settings.payment.alipayForceQRCodeHint")
                }}</span>
              </div>
            </div>
          </div>
          <!-- Row 4: Enabled payment types (provider badges like sub2apipay) -->
          <div>
            <label class="input-label">{{
              t("admin.settings.payment.enabledPaymentTypes")
            }}</label>
            <div class="mt-1.5 flex flex-wrap gap-2">
              <button
                v-for="pt in allPaymentTypes"
                :key="pt.value"
                type="button"
                @click="togglePaymentType(pt.value)"
                :class="[
                  'rounded-lg border px-3 py-1.5 text-sm font-medium transition-all',
                  isPaymentTypeEnabled(pt.value)
                    ? 'border-primary-500 bg-primary-500 text-white shadow-sm'
                    : 'border-gray-300 bg-white text-gray-600 hover:border-gray-400 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-dark-500',
                ]"
              >
                {{ pt.label }}
              </button>
            </div>
            <p class="mt-2 text-xs text-gray-400 dark:text-gray-500">
              {{ t("admin.settings.payment.enabledPaymentTypesHint") }}
              <a
                :href="paymentMethodsHref"
                target="_blank"
                rel="noopener noreferrer"
                class="ml-1 text-primary-500 hover:text-primary-600 dark:text-primary-400 dark:hover:text-primary-300"
              >
                {{ t("admin.settings.payment.findProvider") }}
                <svg
                  class="mb-0.5 ml-0.5 inline h-3 w-3"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
                  />
                </svg>
              </a>
            </p>
          </div>
          <!-- Row 5: Help image + text -->
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.helpImage")
              }}</label>
              <ImageUpload
                v-model="form.payment_help_image_url"
                :upload-label="t('admin.settings.site.uploadImage')"
                :remove-label="t('admin.settings.site.remove')"
                :placeholder="
                  t('admin.settings.payment.helpImagePlaceholder')
                "
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.settings.payment.helpText")
              }}</label>
              <textarea
                v-model="form.payment_help_text"
                rows="3"
                class="input"
                :placeholder="
                  t('admin.settings.payment.helpTextPlaceholder')
                "
              ></textarea>
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- Provider Management -->
    <PaymentProviderList
      v-if="form.payment_enabled"
      :providers="providers"
      :loading="providersLoading"
      :can-create="hasAnyPaymentTypeEnabled"
      :enabled-payment-types="form.payment_enabled_types"
      :all-payment-types="allPaymentTypes"
      :redirect-label="t('admin.settings.payment.easypayRedirect')"
      @refresh="loadProviders"
      @create="openCreateProvider"
      @edit="openEditProvider"
      @delete="confirmDeleteProvider"
      @toggle-field="handleToggleField"
      @toggle-type="handleToggleType"
      @reorder="handleReorderProviders"
    />

    <!-- Provider dialogs -->
    <PaymentProviderDialog
      ref="providerDialogRef"
      :show="showProviderDialog"
      :saving="providerSaving"
      :editing="editingProvider"
      :all-key-options="providerKeyOptions"
      :enabled-key-options="enabledProviderKeyOptions"
      :all-payment-types="allPaymentTypes"
      :redirect-label="t('admin.settings.payment.easypayRedirect')"
      @close="showProviderDialog = false"
      @save="handleSaveProvider"
    />
    <ConfirmDialog
      :show="showDeleteProviderDialog"
      :title="t('admin.settings.payment.deleteProvider')"
      :message="t('admin.settings.payment.deleteProviderConfirm')"
      :confirm-text="t('common.delete')"
      danger
      @confirm="handleDeleteProvider"
      @cancel="showDeleteProviderDialog = false"
    />
  </div>
</template>
