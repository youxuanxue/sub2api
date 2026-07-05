<template>
  <div class="space-y-6">

    <!-- Channel Monitor -->
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('admin.settings.features.channelMonitor.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.features.channelMonitor.description') }}
        </p>
        <p class="mt-1.5 text-xs">
          <router-link
            to="/admin/channels/monitor"
            class="inline-flex items-center gap-1 text-primary-600 hover:underline dark:text-primary-400"
          >
            {{ t('admin.settings.features.channelMonitor.configureLink') }}
            <span aria-hidden="true">&rarr;</span>
          </router-link>
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.features.channelMonitor.enabled') }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.features.channelMonitor.enabledHint') }}
            </p>
          </div>
          <Toggle v-model="form.channel_monitor_enabled" />
        </div>

        <div v-if="form.channel_monitor_enabled">
          <label class="input-label">
            {{ t('admin.settings.features.channelMonitor.defaultInterval') }}
            <span class="text-red-500">*</span>
          </label>
          <input
            v-model.number="form.channel_monitor_default_interval_seconds"
            type="number"
            min="15"
            max="3600"
            class="input"
          />
          <p class="mt-1 text-xs text-gray-400">
            {{ t('admin.settings.features.channelMonitor.defaultIntervalHint') }}
          </p>
        </div>
      </div>
    </div>

    <!-- Available Channels -->
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('admin.settings.features.availableChannels.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.features.availableChannels.description') }}
        </p>
        <p class="mt-1.5 text-xs">
          <router-link
            to="/admin/channels/pricing"
            class="inline-flex items-center gap-1 text-primary-600 hover:underline dark:text-primary-400"
          >
            {{ t('admin.settings.features.availableChannels.configureLink') }}
            <span aria-hidden="true">&rarr;</span>
          </router-link>
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.features.availableChannels.enabled') }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.features.availableChannels.enabledHint') }}
            </p>
          </div>
          <Toggle v-model="form.available_channels_enabled" />
        </div>
      </div>
    </div>

    <!-- Risk Control -->
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('admin.settings.features.riskControl.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.features.riskControl.description') }}
        </p>
        <p class="mt-1.5 text-xs">
          <router-link
            to="/admin/risk-control"
            class="inline-flex items-center gap-1 text-primary-600 hover:underline dark:text-primary-400"
          >
            {{ t('admin.settings.features.riskControl.configureLink') }}
            <span aria-hidden="true">&rarr;</span>
          </router-link>
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.features.riskControl.enabled') }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.features.riskControl.enabledHint') }}
            </p>
          </div>
          <Toggle v-model="form.risk_control_enabled" />
        </div>

        <div class="flex items-center justify-between">
          <div>
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.features.riskControl.cyberSessionBlock') }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.features.riskControl.cyberSessionBlockHint') }}
            </p>
          </div>
          <Toggle v-model="form.cyber_session_block_enabled" />
        </div>

        <div v-if="form.cyber_session_block_enabled">
          <label class="input-label">
            {{ t('admin.settings.features.riskControl.cyberSessionBlockTTL') }}
            <span class="text-red-500">*</span>
          </label>
          <input
            v-model.number="form.cyber_session_block_ttl_seconds"
            type="number"
            min="1"
            class="input"
          />
        </div>
      </div>
    </div>

    <!-- Affiliate (邀请返利) feature card -->
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('admin.settings.features.affiliate.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.features.affiliate.description') }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.features.affiliate.enabled') }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.features.affiliate.enabledHint') }}
            </p>
          </div>
          <Toggle v-model="form.affiliate_enabled" />
        </div>

        <div v-if="form.affiliate_enabled" class="space-y-6">
          <div>
            <label class="input-label">
              {{ t('admin.settings.features.affiliate.rebateRate') }}
            </label>
            <div class="relative">
              <input
                v-model.number="form.affiliate_rebate_rate"
                type="number"
                step="0.01"
                min="0"
                max="100"
                class="input pr-8"
                placeholder="20"
              />
              <span class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-gray-400">%</span>
            </div>
            <p class="mt-1 text-xs text-gray-400">
              {{ t('admin.settings.features.affiliate.rebateRateHint') }}
            </p>
          </div>

          <div>
            <label class="input-label">
              {{ t('admin.settings.features.affiliate.freezeHours') }}
            </label>
            <input
              v-model.number="form.affiliate_rebate_freeze_hours"
              type="number"
              step="1"
              min="0"
              max="720"
              class="input"
            />
            <p class="mt-1 text-xs text-gray-400">
              {{ t('admin.settings.features.affiliate.freezeHoursDesc') }}
            </p>
          </div>

          <div>
            <label class="input-label">
              {{ t('admin.settings.features.affiliate.durationDays') }}
            </label>
            <input
              v-model.number="form.affiliate_rebate_duration_days"
              type="number"
              step="1"
              min="0"
              max="3650"
              class="input"
            />
            <p class="mt-1 text-xs text-gray-400">
              {{ t('admin.settings.features.affiliate.durationDaysDesc') }}
            </p>
          </div>

          <div>
            <label class="input-label">
              {{ t('admin.settings.features.affiliate.perInviteeCap') }}
            </label>
            <input
              v-model.number="form.affiliate_rebate_per_invitee_cap"
              type="number"
              step="0.01"
              min="0"
              class="input"
            />
            <p class="mt-1 text-xs text-gray-400">
              {{ t('admin.settings.features.affiliate.perInviteeCapDesc') }}
            </p>
          </div>

          <!-- 专属用户管理 -->
          <div class="border-t border-gray-100 pt-6 dark:border-dark-700">
            <div class="mb-3 flex items-center justify-between">
              <div>
                <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
                  {{ t('admin.settings.features.affiliate.customUsers.title') }}
                </h3>
                <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.settings.features.affiliate.customUsers.description') }}
                </p>
              </div>
              <button
                type="button"
                class="btn btn-primary btn-sm"
                @click="openAffiliateModal(null)"
              >
                + {{ t('admin.settings.features.affiliate.customUsers.addButton') }}
              </button>
            </div>

            <div class="mb-3 flex items-center gap-2">
              <input
                v-model="affiliateState.search"
                type="text"
                class="input flex-1"
                :placeholder="t('admin.settings.features.affiliate.customUsers.searchPlaceholder')"
                @input="onAffiliateSearchInput"
              />
              <button
                v-if="affiliateState.selected.length > 0"
                type="button"
                class="btn btn-secondary btn-sm"
                @click="openAffiliateBatchModal"
              >
                {{ t('admin.settings.features.affiliate.customUsers.batchButton', { count: affiliateState.selected.length }) }}
              </button>
            </div>

            <div class="overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700">
              <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
                <thead class="bg-gray-50 dark:bg-dark-800">
                  <tr>
                    <th class="px-3 py-2 text-left">
                      <input
                        type="checkbox"
                        :checked="affiliateState.entries.length > 0 && affiliateState.selected.length === affiliateState.entries.length"
                        @change="toggleAffiliateSelectAll"
                      />
                    </th>
                    <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500">{{ t('admin.settings.features.affiliate.customUsers.col.email') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500">{{ t('admin.settings.features.affiliate.customUsers.col.username') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500">{{ t('admin.settings.features.affiliate.customUsers.col.code') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500">{{ t('admin.settings.features.affiliate.customUsers.col.rate') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500">{{ t('admin.settings.features.affiliate.customUsers.col.actions') }}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900">
                  <tr v-if="affiliateState.loading">
                    <td colspan="6" class="px-3 py-6 text-center text-sm text-gray-500">
                      {{ t('common.loading') }}
                    </td>
                  </tr>
                  <tr v-else-if="affiliateState.entries.length === 0">
                    <td colspan="6" class="px-3 py-6 text-center text-sm text-gray-500">
                      {{ t('admin.settings.features.affiliate.customUsers.empty') }}
                    </td>
                  </tr>
                  <tr v-for="entry in affiliateState.entries" :key="entry.user_id">
                    <td class="px-3 py-2">
                      <input
                        type="checkbox"
                        :checked="affiliateState.selected.includes(entry.user_id)"
                        @change="toggleAffiliateSelect(entry.user_id)"
                      />
                    </td>
                    <td class="px-3 py-2 text-sm text-gray-900 dark:text-white">{{ entry.email }}</td>
                    <td class="px-3 py-2 text-sm text-gray-600 dark:text-gray-300">{{ entry.username }}</td>
                    <td class="px-3 py-2 text-sm font-mono">
                      {{ entry.aff_code }}
                      <span
                        v-if="entry.aff_code_custom"
                        class="ml-1 inline-block rounded bg-primary-100 px-1.5 py-0.5 text-[10px] font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                      >{{ t('admin.settings.features.affiliate.customUsers.customBadge') }}</span>
                    </td>
                    <td class="px-3 py-2 text-sm">
                      <span v-if="entry.aff_rebate_rate_percent != null">{{ entry.aff_rebate_rate_percent }}%</span>
                      <span v-else class="text-gray-400">{{ t('admin.settings.features.affiliate.customUsers.useGlobal') }}</span>
                    </td>
                    <td class="px-3 py-2 text-sm">
                      <div class="flex items-center gap-2">
                        <button type="button" class="text-primary-600 hover:underline" @click="openAffiliateModal(entry)">
                          {{ t('common.edit') }}
                        </button>
                        <button
                          type="button"
                          class="text-red-600 hover:underline"
                          @click="askResetAffiliateUser(entry)"
                        >
                          {{ t('common.delete') }}
                        </button>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>

            <div v-if="affiliateState.total > affiliateState.pageSize" class="mt-3 flex items-center justify-between text-sm">
              <span class="text-gray-500">
                {{ t('admin.settings.features.affiliate.customUsers.totalLabel', { total: affiliateState.total }) }}
              </span>
              <div class="flex items-center gap-2">
                <button
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="affiliateState.page <= 1"
                  @click="changeAffiliatePage(affiliateState.page - 1)"
                >
                  {{ t('pagination.previous') }}
                </button>
                <span class="text-gray-500">{{ affiliateState.page }} / {{ Math.max(1, Math.ceil(affiliateState.total / affiliateState.pageSize)) }}</span>
                <button
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="affiliateState.page >= Math.ceil(affiliateState.total / affiliateState.pageSize)"
                  @click="changeAffiliatePage(affiliateState.page + 1)"
                >
                  {{ t('pagination.next') }}
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Affiliate add/edit modal -->
    <div
      v-if="affiliateModal.open"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      @click.self="closeAffiliateModal"
    >
      <div class="w-full max-w-md rounded-lg bg-white p-6 shadow-xl dark:bg-dark-900">
        <h3 class="mb-4 text-lg font-semibold">
          {{ affiliateModal.mode === 'add' ? t('admin.settings.features.affiliate.modal.addTitle') : t('admin.settings.features.affiliate.modal.editTitle') }}
        </h3>
        <div class="space-y-4">
          <div v-if="affiliateModal.mode === 'add'">
            <label class="input-label">{{ t('admin.settings.features.affiliate.modal.userLabel') }}</label>
            <!-- Chip showing the picked user; clicking it re-opens the search -->
            <div
              v-if="affiliateModal.selectedUser"
              class="flex items-center justify-between rounded-md border border-primary-200 bg-primary-50 px-3 py-2 dark:border-primary-700/50 dark:bg-primary-900/20"
            >
              <div class="text-sm">
                <span class="font-medium text-gray-900 dark:text-white">{{ affiliateModal.selectedUser.email }}</span>
                <span class="ml-1 text-xs text-gray-500">({{ affiliateModal.selectedUser.username }})</span>
              </div>
              <button
                type="button"
                class="text-lg leading-none text-gray-400 hover:text-red-600"
                :title="t('admin.settings.features.affiliate.modal.changeUser')"
                @click="clearSelectedAffiliateUser"
              >
                &times;
              </button>
            </div>
            <!-- Search input + result dropdown — hidden once a selection is made -->
            <template v-else>
              <input
                v-model="affiliateModal.userQuery"
                type="text"
                class="input"
                :placeholder="t('admin.settings.features.affiliate.modal.userPlaceholder')"
                @input="onAffiliateUserSearchInput"
              />
              <div
                v-if="affiliateModal.userResults.length > 0"
                class="mt-1 max-h-40 overflow-y-auto rounded border border-gray-200 dark:border-dark-700"
              >
                <button
                  v-for="u in affiliateModal.userResults"
                  :key="u.id"
                  type="button"
                  class="w-full px-3 py-1.5 text-left text-sm hover:bg-gray-100 dark:hover:bg-dark-800"
                  @click="selectAffiliateUser(u)"
                >
                  {{ u.email }} <span class="text-xs text-gray-500">({{ u.username }})</span>
                </button>
              </div>
            </template>
          </div>
          <div v-else>
            <label class="input-label">{{ t('admin.settings.features.affiliate.modal.userLabel') }}</label>
            <input
              type="text"
              class="input"
              :value="affiliateModal.editingEntry ? affiliateModal.editingEntry.email : ''"
              disabled
            />
          </div>

          <div>
            <label class="input-label">{{ t('admin.settings.features.affiliate.modal.codeLabel') }}</label>
            <input
              v-model="affiliateModal.code"
              type="text"
              class="input font-mono"
              :placeholder="t('admin.settings.features.affiliate.modal.codePlaceholder')"
              maxlength="32"
            />
            <p class="mt-1 text-xs text-gray-400">
              {{ t('admin.settings.features.affiliate.modal.codeHint') }}
            </p>
          </div>

          <div>
            <label class="input-label">{{ t('admin.settings.features.affiliate.modal.rateLabel') }}</label>
            <div class="relative">
              <input
                v-model="affiliateModal.rate"
                type="number"
                step="0.01"
                min="0"
                max="100"
                class="input pr-8"
                :placeholder="t('admin.settings.features.affiliate.modal.ratePlaceholder')"
              />
              <span class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-gray-400">%</span>
            </div>
            <p class="mt-1 text-xs text-gray-400">
              {{ t('admin.settings.features.affiliate.modal.rateHint') }}
            </p>
          </div>
        </div>

        <div class="mt-6 flex items-center justify-between gap-3">
          <p
            v-if="!affiliateModalCanSubmit"
            class="text-xs text-gray-500 dark:text-gray-400"
          >
            {{ t('admin.settings.features.affiliate.modal.errorEmpty') }}
          </p>
          <span v-else></span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary" @click="closeAffiliateModal">
              {{ t('common.cancel') }}
            </button>
            <button
              type="button"
              class="btn btn-primary"
              :disabled="affiliateModal.saving || !affiliateModalCanSubmit"
              @click="submitAffiliateModal"
            >
              {{ affiliateModal.saving ? t('common.saving') : t('common.save') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Affiliate batch rate modal -->
    <div
      v-if="affiliateBatchModal.open"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      @click.self="affiliateBatchModal.open = false"
    >
      <div class="w-full max-w-md rounded-lg bg-white p-6 shadow-xl dark:bg-dark-900">
        <h3 class="mb-4 text-lg font-semibold">
          {{ t('admin.settings.features.affiliate.batchModal.title', { count: affiliateState.selected.length }) }}
        </h3>
        <p class="mb-4 text-sm text-gray-500">
          {{ t('admin.settings.features.affiliate.batchModal.hint') }}
        </p>
        <div class="relative">
          <input
            v-model="affiliateBatchModal.rate"
            type="number"
            step="0.01"
            min="0"
            max="100"
            class="input pr-8"
            :placeholder="t('admin.settings.features.affiliate.batchModal.placeholder')"
          />
          <span class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-gray-400">%</span>
        </div>
        <p class="mt-2 text-xs text-gray-400">
          {{ t('admin.settings.features.affiliate.batchModal.clearHint') }}
        </p>
        <div class="mt-6 flex justify-end gap-2">
          <button type="button" class="btn btn-secondary" @click="affiliateBatchModal.open = false">
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            :disabled="affiliateBatchModal.saving"
            @click="submitAffiliateBatchModal"
          >
            {{ affiliateBatchModal.saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Affiliate confirm dialog -->
    <ConfirmDialog
      :show="affiliateConfirmDialog.show"
      :title="affiliateConfirmDialog.title"
      :message="affiliateConfirmDialog.message"
      :confirm-text="affiliateConfirmDialog.confirmText"
      danger
      @confirm="handleAffiliateConfirm"
      @cancel="cancelAffiliateConfirm"
    />

  </div>
</template>

<script setup lang="ts">
import { reactive, computed, watch } from "vue";
import { useI18n } from "vue-i18n";
import Toggle from "@/components/common/Toggle.vue";
import ConfirmDialog from "@/components/common/ConfirmDialog.vue";
import {
  affiliatesAPI,
  type AffiliateAdminEntry,
  type SimpleUser as AffiliateSimpleUser,
} from "@/api/admin/affiliates";
import { extractApiErrorMessage } from "@/utils/apiError";
import { useAppStore } from "@/stores";
import { useSettingsState } from "@/composables/useSettingsState";

const { t } = useI18n();
const appStore = useAppStore();
const { form } = useSettingsState();

// =========================
// Affiliate (邀请返利) 专属用户管理
// =========================

interface AffiliateState {
  loading: boolean;
  entries: AffiliateAdminEntry[];
  total: number;
  page: number;
  pageSize: number;
  search: string;
  selected: number[];
  searchTimer: number | null;
}

const affiliateState = reactive<AffiliateState>({
  loading: false,
  entries: [],
  total: 0,
  page: 1,
  pageSize: 20,
  search: "",
  selected: [],
  searchTimer: null,
});

// `rate` is typed as string|number because <input type="number"> makes Vue's
// v-model auto-cast the bound value to a Number on every keystroke. We keep
// both shapes and normalize at read time.
interface AffiliateModalState {
  open: boolean;
  mode: "add" | "edit";
  saving: boolean;
  userQuery: string;
  userResults: AffiliateSimpleUser[];
  selectedUser: AffiliateSimpleUser | null;
  editingEntry: AffiliateAdminEntry | null;
  code: string;
  rate: string | number;
  searchTimer: number | null;
}

const affiliateModal = reactive<AffiliateModalState>({
  open: false,
  mode: "add",
  saving: false,
  userQuery: "",
  userResults: [],
  selectedUser: null,
  editingEntry: null,
  code: "",
  rate: "",
  searchTimer: null,
});

const affiliateBatchModal = reactive<{
  open: boolean;
  saving: boolean;
  rate: string | number;
}>({
  open: false,
  saving: false,
  rate: "",
});

// affiliateConfirmDialog drives the project-standard <ConfirmDialog>. We can't
// `await` the user's response from the dialog component, so the confirm action
// runs from the @confirm callback once the user clicks the dialog's confirm
// button.
const affiliateConfirmDialog = reactive<{
  show: boolean;
  title: string;
  message: string;
  confirmText: string;
  pending: (() => Promise<unknown>) | null;
}>({
  show: false,
  title: "",
  message: "",
  confirmText: "",
  pending: null,
});

function openAffiliateConfirm(
  title: string,
  message: string,
  confirmText: string,
  fn: () => Promise<unknown>,
) {
  affiliateConfirmDialog.title = title;
  affiliateConfirmDialog.message = message;
  affiliateConfirmDialog.confirmText = confirmText;
  affiliateConfirmDialog.pending = fn;
  affiliateConfirmDialog.show = true;
}

async function handleAffiliateConfirm() {
  const fn = affiliateConfirmDialog.pending;
  affiliateConfirmDialog.show = false;
  affiliateConfirmDialog.pending = null;
  if (!fn) return;
  try {
    await fn();
    appStore.showSuccess(t("common.saved"));
    await loadAffiliateUsers();
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t("common.error")));
  }
}

function cancelAffiliateConfirm() {
  affiliateConfirmDialog.show = false;
  affiliateConfirmDialog.pending = null;
}

// debounceTimer wires a single timer slot to a callback with a delay,
// canceling any pending invocation. Used for type-as-you-go search inputs.
function debounceTimer(slot: { searchTimer: number | null }, delayMs: number, run: () => void) {
  if (slot.searchTimer != null) window.clearTimeout(slot.searchTimer);
  slot.searchTimer = window.setTimeout(run, delayMs);
}

// parseRebateRate validates 0-100 numeric input. Returns the parsed number on
// success, null when the field is empty (caller decides empty semantics), or
// undefined on invalid input (after surfacing a toast).
//
// Accepts unknown because <input type="number"> makes Vue's v-model coerce
// the value to Number on each keystroke (e.g. typing "30" lands a `30: number`
// in state, not a `"30": string`). String("") and (30).trim() would crash, so
// we normalize here instead of forcing every caller to remember.
function parseRebateRate(raw: unknown): number | null | undefined {
  const s = String(raw ?? "").trim();
  if (s === "") return null;
  const parsed = Number(s);
  if (Number.isNaN(parsed) || parsed < 0 || parsed > 100) {
    appStore.showError(t("admin.settings.features.affiliate.modal.errorBadRate"));
    return undefined;
  }
  return parsed;
}

async function loadAffiliateUsers() {
  affiliateState.loading = true;
  try {
    const res = await affiliatesAPI.listUsers({
      page: affiliateState.page,
      page_size: affiliateState.pageSize,
      search: affiliateState.search,
    });
    affiliateState.entries = res.items ?? [];
    affiliateState.total = res.total ?? 0;
    // Drop selections that are no longer visible.
    const visibleIds = new Set(affiliateState.entries.map((e) => e.user_id));
    affiliateState.selected = affiliateState.selected.filter((id) => visibleIds.has(id));
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t("common.error")));
  } finally {
    affiliateState.loading = false;
  }
}

function onAffiliateSearchInput() {
  debounceTimer(affiliateState, 300, () => {
    affiliateState.page = 1;
    loadAffiliateUsers();
  });
}

function changeAffiliatePage(page: number) {
  if (page < 1) return;
  affiliateState.page = page;
  loadAffiliateUsers();
}

function toggleAffiliateSelectAll(e: Event) {
  const checked = (e.target as HTMLInputElement).checked;
  affiliateState.selected = checked ? affiliateState.entries.map((entry) => entry.user_id) : [];
}

function toggleAffiliateSelect(userId: number) {
  const idx = affiliateState.selected.indexOf(userId);
  if (idx >= 0) affiliateState.selected.splice(idx, 1);
  else affiliateState.selected.push(userId);
}

// openAffiliateModal opens the add/edit modal, prefilling fields from the
// edited entry when present and resetting them otherwise.
function openAffiliateModal(entry: AffiliateAdminEntry | null) {
  affiliateModal.open = true;
  affiliateModal.mode = entry ? "edit" : "add";
  affiliateModal.userQuery = "";
  affiliateModal.userResults = [];
  affiliateModal.selectedUser = null;
  affiliateModal.editingEntry = entry;
  affiliateModal.code = entry?.aff_code_custom ? entry.aff_code : "";
  affiliateModal.rate =
    entry?.aff_rebate_rate_percent != null ? String(entry.aff_rebate_rate_percent) : "";
}

function closeAffiliateModal() {
  affiliateModal.open = false;
  if (affiliateModal.searchTimer != null) {
    window.clearTimeout(affiliateModal.searchTimer);
    affiliateModal.searchTimer = null;
  }
}

function onAffiliateUserSearchInput() {
  const q = affiliateModal.userQuery.trim();
  if (!q) {
    affiliateModal.userResults = [];
    return;
  }
  debounceTimer(affiliateModal, 300, async () => {
    try {
      affiliateModal.userResults = await affiliatesAPI.lookupUsers(q);
    } catch (err) {
      appStore.showError(extractApiErrorMessage(err, t("common.error")));
    }
  });
}

// selectAffiliateUser picks a user from the dropdown and collapses the search
// UI. Clearing the result list also clears the visual dropdown.
function selectAffiliateUser(user: AffiliateSimpleUser) {
  affiliateModal.selectedUser = user;
  affiliateModal.userQuery = "";
  affiliateModal.userResults = [];
}

function clearSelectedAffiliateUser() {
  affiliateModal.selectedUser = null;
}

// affiliateModalCanSubmit guards the Save button: must have a user picked AND
// produce at least one field change. Without this the admin could "save" an
// empty payload that silently does nothing — the user reported exactly that
// confusion.
const affiliateModalCanSubmit = computed(() => {
  if (affiliateModal.mode === "add") {
    if (!affiliateModal.selectedUser) return false;
  } else if (!affiliateModal.editingEntry) {
    return false;
  }
  const codeFilled = affiliateModal.code.trim() !== "";
  const rateFilled = String(affiliateModal.rate ?? "").trim() !== "";
  if (codeFilled || rateFilled) return true;
  // Edit mode + empty rate input is a meaningful "clear" only if the user
  // currently has an exclusive rate to clear.
  return (
    affiliateModal.mode === "edit" &&
    affiliateModal.editingEntry?.aff_rebate_rate_percent != null
  );
});

async function submitAffiliateModal() {
  if (!affiliateModalCanSubmit.value) {
    // Should be unreachable because the button is disabled, but keep a guard.
    appStore.showError(t("admin.settings.features.affiliate.modal.errorEmpty"));
    return;
  }

  let userId: number;
  if (affiliateModal.mode === "add") {
    userId = affiliateModal.selectedUser!.id;
  } else {
    userId = affiliateModal.editingEntry!.user_id;
  }

  const payload: Parameters<typeof affiliatesAPI.updateUserSettings>[1] = {};
  const codeRaw = affiliateModal.code.trim();
  if (codeRaw) payload.aff_code = codeRaw.toUpperCase();

  const rateInput = parseRebateRate(affiliateModal.rate);
  if (rateInput === undefined) return; // toast already shown
  if (rateInput === null) {
    if (affiliateModal.mode === "edit" && affiliateModal.editingEntry?.aff_rebate_rate_percent != null) {
      payload.clear_rebate_rate = true;
    }
  } else {
    payload.aff_rebate_rate_percent = rateInput;
  }

  affiliateModal.saving = true;
  try {
    await affiliatesAPI.updateUserSettings(userId, payload);
    appStore.showSuccess(t("common.saved"));
    closeAffiliateModal();
    affiliateState.page = 1;
    await loadAffiliateUsers();
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t("common.error")));
  } finally {
    affiliateModal.saving = false;
  }
}

// askResetAffiliateUser prompts via the project ConfirmDialog, then on confirm
// calls the backend "reset all" endpoint that clears both the exclusive rate
// AND regenerates the invite code as a system random one.
function askResetAffiliateUser(entry: AffiliateAdminEntry) {
  openAffiliateConfirm(
    t("admin.settings.features.affiliate.customUsers.resetTitle"),
    t("admin.settings.features.affiliate.customUsers.resetMessage", {
      email: entry.email || `#${entry.user_id}`,
    }),
    t("common.delete"),
    () => affiliatesAPI.clearUserSettings(entry.user_id),
  );
}

function openAffiliateBatchModal() {
  if (affiliateState.selected.length === 0) return;
  affiliateBatchModal.open = true;
  affiliateBatchModal.rate = "";
}

async function submitAffiliateBatchModal() {
  const rateInput = parseRebateRate(affiliateBatchModal.rate);
  if (rateInput === undefined) return;
  const userIDs = [...affiliateState.selected];
  const payload: Parameters<typeof affiliatesAPI.batchSetRate>[0] =
    rateInput === null
      ? { user_ids: userIDs, clear: true }
      : { user_ids: userIDs, aff_rebate_rate_percent: rateInput };

  affiliateBatchModal.saving = true;
  try {
    await affiliatesAPI.batchSetRate(payload);
    appStore.showSuccess(t("common.saved"));
    affiliateBatchModal.open = false;
    affiliateState.selected = [];
    await loadAffiliateUsers();
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t("common.error")));
  } finally {
    affiliateBatchModal.saving = false;
  }
}

// Load the per-user table the first time the affiliate switch is observed
// as enabled. The form starts disabled and is updated to the server's value
// after the settings load — so this fires either when the saved value is
// truthy on first paint, or when the admin manually toggles it on.
watch(
  () => form.affiliate_enabled,
  (enabled, prev) => {
    if (enabled && !prev) {
      loadAffiliateUsers();
    }
  },
);
</script>
