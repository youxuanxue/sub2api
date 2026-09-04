<template>
  <!--
    Fill AppLayout main like TablePageLayout (header 64px + lg:p-8). Title lives in
    AppHeader only — do not reintroduce an in-page h1/description shell.
  -->
  <div
    data-test="supplier-sources-page"
    class="supplier-sources-page flex w-full min-w-0 flex-col gap-4"
  >
    <div class="flex shrink-0 flex-wrap items-center justify-end gap-2">
      <button
        data-test="priority-preview-button"
        type="button"
        class="rounded-lg border border-gray-300 px-4 py-2 text-sm dark:border-dark-600"
        :disabled="previewing"
        @click="loadPriorityPreview"
      >
        {{ t('admin.supplierSources.globalPriorityPreview') }}
      </button>
    </div>

    <section
      v-if="priorityPreview"
      data-test="priority-preview"
      class="shrink-0 rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800"
    >
      <h2 class="font-medium">{{ t('admin.supplierSources.globalPriorityPreview') }}</h2>
      <p v-if="priorityPreview.entries.length === 0" class="mt-2 text-sm text-gray-500">
        {{ t('admin.supplierSources.previewEmpty') }}
      </p>
      <div v-else class="mt-3 overflow-x-auto">
        <table class="min-w-full text-left text-sm">
          <thead class="text-gray-500">
            <tr>
              <th class="px-2 py-2">{{ t('admin.supplierSources.sources') }}</th>
              <th class="px-2 py-2">{{ t('admin.supplierSources.discountBand') }}</th>
              <th class="px-2 py-2">priority</th>
              <th class="px-2 py-2">{{ t('admin.supplierSources.clientModel') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry in priorityPreview.entries" :key="`${entry.source_id}-${entry.discount_band}`">
              <td class="px-2 py-2">{{ entry.supplier_name }}/{{ entry.supplier_lane }}</td>
              <td class="px-2 py-2">{{ entry.discount_band }}</td>
              <td class="px-2 py-2">{{ entry.priority }}</td>
              <td class="px-2 py-2">{{ entry.client_model_ids.join(', ') }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <ul v-if="priorityPreview.warnings.length" class="mt-3 space-y-1 text-sm text-amber-700">
        <li v-for="warning in priorityPreview.warnings" :key="`${warning.code}-${warning.priority}`">
          {{ warning.code }} · priority {{ warning.priority }} · #{{ warning.source_ids.join(', #') }}
        </li>
      </ul>
    </section>

    <div
      data-test="supplier-sources-workspace"
      class="grid min-h-0 flex-1 items-stretch gap-4 lg:grid-cols-[minmax(16rem,20rem)_minmax(0,1fr)]"
    >
      <section
        data-test="source-list"
        class="flex max-h-[min(22rem,50vh)] min-h-0 flex-col overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800 lg:max-h-none lg:h-full"
      >
        <div class="shrink-0 space-y-2 border-b border-gray-200 p-3 dark:border-dark-700">
          <div class="flex items-center justify-between gap-2">
            <h2 class="min-w-0 truncate font-medium">
              {{ t('admin.supplierSources.sources') }}
              <span
                v-if="sources.length > 0"
                data-test="source-list-count"
                class="ml-1 text-xs font-normal text-gray-500"
              >
                {{ listCountLabel }}
              </span>
            </h2>
            <button
              data-test="new-source"
              class="shrink-0 text-sm text-primary-600"
              type="button"
              @click="resetForm"
            >
              {{ t('admin.supplierSources.newSource') }}
            </button>
          </div>
          <input
            v-if="sources.length > 0"
            v-model="listQuery"
            data-test="source-search"
            type="search"
            :placeholder="t('admin.supplierSources.searchPlaceholder')"
            class="w-full rounded-lg border px-2.5 py-1.5 text-sm"
          />
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto p-2">
          <div v-if="loading" class="px-1 py-2 text-sm text-gray-500">{{ t('common.loading') }}</div>
          <div v-else-if="sources.length === 0" class="px-1 py-2 text-sm text-gray-500">
            {{ t('admin.supplierSources.empty') }}
          </div>
          <div v-else-if="filteredSources.length === 0" data-test="source-search-empty" class="px-1 py-2 text-sm text-gray-500">
            {{ t('admin.supplierSources.noSearchResults') }}
          </div>
          <div v-else class="space-y-3">
            <div v-for="group in groupedSources" :key="group.supplier">
              <div
                v-if="groupedSources.length > 1"
                class="px-1 pb-1 text-[11px] font-medium text-gray-400"
              >
                {{ group.supplier }}
                <span class="font-normal normal-case tracking-normal">{{ group.sources.length }}</span>
              </div>
              <div class="space-y-1">
                <button
                  v-for="source in group.sources"
                  :key="source.id"
                  :data-test="`source-select-${source.id}`"
                  type="button"
                  class="w-full rounded-md border px-2.5 py-1.5 text-left"
                  :class="selected?.id === source.id
                    ? 'border-primary-500 bg-primary-50 dark:bg-primary-950/20'
                    : 'border-gray-200 dark:border-dark-600'"
                  @click="selectSource(source)"
                >
                  <div class="truncate text-sm font-medium">
                    <template v-if="groupedSources.length > 1">{{ source.supplier_lane }}</template>
                    <template v-else>{{ source.supplier_name }} · {{ source.supplier_lane }}</template>
                  </div>
                  <div class="truncate text-[11px] text-gray-500">
                    priority {{ source.base_priority }} · {{ source.models.length }} models
                  </div>
                </button>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section
        ref="editorEl"
        data-test="source-editor"
        class="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800 lg:h-full"
      >
        <form class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 sm:p-5" @submit.prevent="save">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div>
              <h2 data-test="editor-title" class="font-medium">{{ editorTitle }}</h2>
              <p v-if="copiedFrom" data-test="copy-hint" class="mt-1 text-xs text-gray-500">
                {{ t('admin.supplierSources.copyHint') }}
              </p>
            </div>
            <button
              v-if="selected"
              data-test="copy-source"
              type="button"
              class="rounded-lg border border-gray-300 px-3 py-1.5 text-sm dark:border-dark-600"
              @click="copySelected"
            >
              {{ t('admin.supplierSources.copyAsNew') }}
            </button>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="text-sm">
              {{ t('admin.supplierSources.supplierName') }}
              <input v-model.trim="form.supplier_name" data-test="supplier-name" required class="mt-1 w-full rounded-lg border px-3 py-2" />
            </label>
            <label class="text-sm">
              {{ t('admin.supplierSources.supplierLane') }}
              <input v-model.trim="form.supplier_lane" data-test="supplier-lane" required class="mt-1 w-full rounded-lg border px-3 py-2" />
              <span class="mt-1 block text-xs text-gray-500">{{ t('admin.supplierSources.supplierLaneHint') }}</span>
            </label>
          </div>

          <label class="block text-sm">
            {{ t('admin.supplierSources.channelType') }}
            <select
              v-model.number="form.channel_type"
              data-test="channel-type"
              required
              class="mt-1 w-full rounded-lg border px-3 py-2"
              :disabled="channelTypesLoading"
              @change="applyChannelTypeDefaultEndpoint(form.channel_type)"
            >
              <option v-if="channelTypesLoading" disabled value="0">{{ t('common.loading') }}</option>
              <option v-for="option in supplierChannelTypeOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </option>
            </select>
            <span class="mt-1 block text-xs text-gray-500">{{ t('admin.supplierSources.channelTypeHint') }}</span>
            <span v-if="channelTypesError" class="mt-1 block text-xs text-red-600">{{ channelTypesError }}</span>
          </label>

          <label class="block text-sm">
            {{ t('admin.supplierSources.endpoint') }}
            <input v-model.trim="form.endpoint" data-test="endpoint" type="url" required class="mt-1 w-full rounded-lg border px-3 py-2" />
          </label>

          <div class="grid gap-4 sm:grid-cols-2">
            <label class="text-sm">
              {{ t('admin.supplierSources.credential') }}
              <input
                v-model="form.credential"
                data-test="credential"
                type="password"
                :required="selected === null"
                autocomplete="new-password"
                class="mt-1 w-full rounded-lg border px-3 py-2"
              />
              <span v-if="selected" class="mt-1 block text-xs text-gray-500">
                {{ t('admin.supplierSources.credentialKeep') }}
              </span>
            </label>
            <label class="text-sm">
              {{ t('admin.supplierSources.basePriority') }}
              <input
                v-model.number="form.base_priority"
                data-test="base-priority"
                type="number"
                required
                class="mt-1 w-full rounded-lg border px-3 py-2"
              />
            </label>
            <label class="text-sm">
              {{ t('admin.supplierSources.accountConcurrency') }}
              <input
                v-model.number="form.account_concurrency"
                data-test="account-concurrency"
                type="number"
                min="1"
                required
                class="mt-1 w-full rounded-lg border px-3 py-2"
              />
            </label>
          </div>

          <label class="block text-sm">
            {{ t('admin.supplierSources.notes') }}
            <textarea v-model="form.notes" data-test="notes" class="mt-1 w-full rounded-lg border px-3 py-2" />
          </label>

          <div class="space-y-3">
            <div
              v-for="(model, index) in form.models"
              :key="index"
              class="grid gap-3 rounded-lg border border-gray-200 p-3 lg:grid-cols-[1fr_1fr_160px_150px_auto]"
            >
              <input
                v-model.trim="model.client_model_id"
                data-test="client-model-id"
                :placeholder="t('admin.supplierSources.clientModel')"
                class="rounded-lg border px-3 py-2"
              />
              <input
                v-model.trim="model.upstream_model_id"
                data-test="upstream-model-id"
                :placeholder="t('admin.supplierSources.upstreamModel')"
                class="rounded-lg border px-3 py-2"
              />
              <input
                v-model.number="model.purchase_ratio"
                data-test="purchase-ratio"
                type="number"
                min="0.000001"
                max="1"
                step="0.000001"
                :placeholder="t('admin.supplierSources.purchaseRatio')"
                class="rounded-lg border px-3 py-2"
              />
              <div class="text-xs text-gray-500">
                <div :data-test="`model-band-${index}`">
                  {{ t('admin.supplierSources.discountBand') }} {{ discountBand(model.purchase_ratio) }}
                </div>
                <div :data-test="`model-priority-${index}`">
                  {{ t('admin.supplierSources.accountPriority') }} {{ modelPriority(model.purchase_ratio) }}
                </div>
              </div>
              <button
                type="button"
                class="text-sm text-red-600"
                :disabled="form.models.length === 1"
                @click="removeModel(index)"
              >
                {{ t('admin.supplierSources.removeModel') }}
              </button>
            </div>
            <button type="button" class="text-sm text-primary-600" @click="addModel">
              {{ t('admin.supplierSources.addModel') }}
            </button>
          </div>

          <p v-if="saveError" class="text-sm text-red-600">{{ saveError }}</p>
          <div class="flex flex-wrap items-center gap-2">
            <button
              v-if="selected"
              data-test="discover-source"
              type="button"
              :disabled="saving || discovering || validating || syncing || blocksDiscoverValidateProject"
              class="rounded-lg border border-gray-300 px-4 py-2 disabled:opacity-50 dark:border-dark-600"
              @click="discoverSelected"
            >
              {{ t('admin.supplierSources.discover') }}
            </button>
            <label
              v-if="selected"
              data-test="discover-channel-scoped"
              class="inline-flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300"
              :title="t('admin.supplierSources.discoverChannelScopedHint')"
            >
              <input
                v-model="discoverChannelScoped"
                type="checkbox"
                class="rounded border-gray-300"
                :disabled="saving || discovering || validating || syncing"
              >
              {{ t('admin.supplierSources.discoverChannelScoped') }}
            </label>
            <button
              data-test="save-source"
              type="submit"
              :disabled="saving || discovering || validating || syncing || !canSaveSelected"
              class="rounded-lg bg-primary-600 px-4 py-2 text-white disabled:opacity-50"
            >
              {{ t('admin.supplierSources.save') }}
            </button>
            <button
              v-if="selected"
              data-test="validate-source"
              type="button"
              :disabled="saving || discovering || validating || syncing || blocksDiscoverValidateProject"
              class="rounded-lg border border-gray-300 px-4 py-2 disabled:opacity-50 dark:border-dark-600"
              @click="validateSelected"
            >
              {{ t('admin.supplierSources.validate') }}
            </button>
            <button
              v-if="selected"
              data-test="sync-source"
              type="button"
              :disabled="saving || discovering || validating || syncing || blocksDiscoverValidateProject"
              class="rounded-lg border border-gray-300 px-4 py-2 disabled:opacity-50 dark:border-dark-600"
              @click="syncSelected"
            >
              {{ t('admin.supplierSources.project') }}
            </button>
            <span
              v-if="selected && blocksDiscoverValidateProject"
              data-test="sync-save-first"
              class="self-center text-sm text-amber-700"
            >
              {{ t('admin.supplierSources.saveBeforeAction') }}
            </span>
          </div>
        </form>

        <div
          v-if="syncError || discoverResult || validateResult || syncResult"
          class="min-h-0 shrink-0 space-y-4 overflow-y-auto border-t border-gray-200 p-4 dark:border-dark-700 sm:p-5"
        >
        <p
          v-if="syncError"
          data-test="sync-error"
          class="rounded-lg border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-950/40 dark:text-red-300"
        >
          {{ syncError }}
        </p>

        <section
          v-if="discoverResult"
          data-test="discover-result"
          class="space-y-4"
        >
          <div class="flex flex-wrap items-center gap-3">
            <h2 class="font-medium">{{ t('admin.supplierSources.discoverPanelTitle') }}</h2>
            <span
              v-if="discoverResult.failed_step"
              data-test="discover-failed-step"
              class="text-sm text-red-600"
            >
              {{ t('admin.supplierSources.failedStep') }}: {{ discoverResult.failed_step }}
            </span>
          </div>
          <p data-test="discover-summary" class="text-sm text-gray-600 dark:text-gray-300">
            {{ t('admin.supplierSources.discoverSummary', {
              upstream: discoverResult.upstream_models.length,
              normalized: discoverResult.normalized_changes.length,
              suggested: discoverResult.suggested_appends.length,
              issues: discoverResult.configured_issues.length,
              rejected: discoverResult.rejected_candidates.length,
            }) }}
          </p>
          <p
            v-if="discoverResult.probe_status === 'running'"
            data-test="discover-candidate-progress"
            class="text-sm text-amber-700"
          >
            {{ t('admin.supplierSources.discoverCandidateProgress', {
              done: discoverResult.probe_done,
              total: discoverResult.probe_total,
            }) }}
          </p>
          <p v-if="discoverNeedsSave" data-test="discover-needs-save" class="text-sm text-amber-700">
            {{ t('admin.supplierSources.discoverNeedsSave') }}
          </p>
          <div v-if="discoverResult.normalized_changes.length">
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.normalizedChanges') }}</h3>
            <ul class="mt-2 space-y-1 text-sm">
              <li
                v-for="change in discoverResult.normalized_changes"
                :key="`${change.from_upstream_model_id}->${change.to_upstream_model_id}`"
              >
                {{ change.from_client_model_id }} / {{ change.from_upstream_model_id }}
                → {{ change.to_client_model_id }} / {{ change.to_upstream_model_id }}
              </li>
            </ul>
          </div>
          <div v-if="discoverResult.suggested_appends.length">
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.suggestedAppends') }}</h3>
            <ul class="mt-2 space-y-1 text-sm">
              <li
                v-for="model in discoverResult.suggested_appends"
                :key="`suggest-${model.upstream_model_id}`"
              >
                {{ model.upstream_model_id }} · ratio {{ model.purchase_ratio ?? '—' }}
              </li>
            </ul>
          </div>
          <div v-if="discoverResult.configured_issues.length">
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.configuredIssues') }}</h3>
            <ul class="mt-2 space-y-1 text-sm">
              <li
                v-for="issue in discoverResult.configured_issues"
                :key="`issue-${issue.upstream_model_id}`"
              >
                {{ issue.client_model_id }} / {{ issue.upstream_model_id }} · {{ issue.reason }}
              </li>
            </ul>
          </div>
          <div v-if="discoverResult.rejected_candidates.length">
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.rejectedCandidates') }}</h3>
            <ul class="mt-2 space-y-1 text-sm text-gray-600 dark:text-gray-300">
              <li
                v-for="item in discoverResult.rejected_candidates"
                :key="`reject-${item.upstream_model_id}`"
              >
                {{ item.upstream_model_id }}
                <span v-if="item.type"> · {{ item.type }}</span>
                · {{ item.reason }}
              </li>
            </ul>
          </div>
          <p
            v-if="discoverEmptyDetail"
            data-test="discover-empty"
            class="text-sm text-gray-500"
          >
            {{ t('admin.supplierSources.discoverEmpty') }}
          </p>
        </section>

        <section
          v-if="validateResult"
          data-test="validate-result"
          class="space-y-4"
        >
          <div class="flex flex-wrap items-center gap-3">
            <h2 class="font-medium">{{ t('admin.supplierSources.validatePanelTitle') }}</h2>
            <span
              v-if="validateResult.failed_step"
              data-test="validate-failed-step"
              class="text-sm text-red-600"
            >
              {{ t('admin.supplierSources.failedStep') }}: {{ validateResult.failed_step }}
            </span>
          </div>
          <div v-if="validateResult.probe_results.length">
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.configuredProbes') }}</h3>
            <ul class="mt-2 space-y-2">
              <li
                v-for="probe in validateResult.probe_results"
                :key="`validated-${probe.client_model_id}-${probe.upstream_model_id}`"
                class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-900"
              >
                <div class="font-medium">{{ probe.client_model_id }} → {{ probe.upstream_model_id }}</div>
                <div class="text-gray-600 dark:text-gray-300">
                  {{ probe.status }}<span v-if="probe.protocol"> · {{ probe.protocol }}</span><span v-if="probe.detail"> · {{ probe.detail }}</span>
                </div>
              </li>
            </ul>
          </div>
        </section>

        <section
          v-if="syncResult"
          data-test="sync-result"
          class="space-y-4"
        >
          <div class="flex flex-wrap items-center gap-3">
            <h2 class="font-medium">{{ t('admin.supplierSources.syncResult') }}</h2>
            <span v-if="syncSucceeded" class="text-sm text-green-700">
              {{ t('admin.supplierSources.syncSucceeded') }}
            </span>
            <span v-if="syncResult.failed_step" class="text-sm text-red-600">
              {{ t('admin.supplierSources.failedStep') }}: {{ syncResult.failed_step }}
            </span>
          </div>

          <div v-if="syncResult.probe_results.length">
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.configuredProbes') }}</h3>
            <ul class="mt-2 space-y-2 text-sm">
              <li
                v-for="probe in syncResult.probe_results"
                :key="`projected-${probe.client_model_id}-${probe.upstream_model_id}`"
                class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900"
              >
                <div class="font-medium">{{ probe.client_model_id }} → {{ probe.upstream_model_id }}</div>
                <div class="text-gray-600 dark:text-gray-300">
                  {{ probe.status }}<span v-if="probe.protocol"> · {{ probe.protocol }}</span><span v-if="probe.detail"> · {{ probe.detail }}</span>
                </div>
              </li>
            </ul>
          </div>

          <div>
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.changes') }}</h3>
            <p v-if="syncResult.changes.length === 0" class="mt-1 text-sm text-gray-500">
              {{ t('admin.supplierSources.noChanges') }}
            </p>
            <ul v-else class="mt-2 space-y-2 text-sm">
              <li v-for="change in syncResult.changes" :key="`${change.account_id}-${change.discount_band}-${change.action}`" class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900">
                <div class="font-medium">#{{ change.account_id }} · {{ change.action }} · band {{ change.discount_band }}</div>
                <div>priority {{ change.priority_before ?? '—' }} → {{ change.priority_after }}</div>
                <div v-if="change.added_models.length">{{ t('admin.supplierSources.addedModels') }}: {{ change.added_models.join(', ') }}</div>
                <div v-if="change.removed_models.length">{{ t('admin.supplierSources.removedModels') }}: {{ change.removed_models.join(', ') }}</div>
              </li>
            </ul>
          </div>
        </section>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import {
  adminAPI,
  type SupplierSourceProbeResult,
  type SupplierSourceValidateResult,
  type SupplierPriorityPreview,
  type SupplierSource,
  type SupplierSourceInput,
  type SupplierSourceModel,
  type SupplierSourceSyncResult,
} from '@/api/admin'
import { useNewApiChannelTypes } from '@/composables/useNewApiChannelTypes'
import { isNewApiUpstreamFetchableChannelType } from '@/constants/newApiUpstreamFetchableChannelTypes'
import { SUPPLIER_DISCOUNT_PRIORITY_STEP } from '@/constants/supplierSource.tk'

const { t } = useI18n()
const route = useRoute()
const { types: channelTypes, loading: channelTypesLoading, error: channelTypesError, load: loadChannelTypes } = useNewApiChannelTypes()
const SUPPLIER_BAIDU_V2_CHANNEL_TYPE = 46

const supplierChannelTypeOptions = computed(() =>
  channelTypes.value
    .filter(item => (
      isNewApiUpstreamFetchableChannelType(item.channel_type)
      || item.channel_type === SUPPLIER_BAIDU_V2_CHANNEL_TYPE
    ))
    .map(item => ({
      value: item.channel_type,
      label: `${item.name} (${item.channel_type})`,
    })),
)

const loading = ref(true)
const saving = ref(false)
const discovering = ref(false)
const validating = ref(false)
const syncing = ref(false)
const previewing = ref(false)
const discoverChannelScoped = ref(false)
const sources = ref<SupplierSource[]>([])
const selected = ref<SupplierSource | null>(null)
const copiedFrom = ref<SupplierSource | null>(null)
const listQuery = ref('')
const editorEl = ref<HTMLElement | null>(null)
const priorityPreview = ref<SupplierPriorityPreview | null>(null)
const syncResult = ref<SupplierSourceSyncResult | null>(null)
const discoverResult = ref<SupplierSourceProbeResult | null>(null)
const validateResult = ref<SupplierSourceValidateResult | null>(null)
const discoverNeedsSave = ref(false)
const syncError = ref('')
const saveError = ref('')

const filteredSources = computed(() => sources.value.filter(source => sourceMatchesQuery(source, listQuery.value)))

const groupedSources = computed(() => {
  const groups: { supplier: string; sources: SupplierSource[] }[] = []
  const index = new Map<string, number>()
  for (const source of filteredSources.value) {
    const existing = index.get(source.supplier_name)
    if (existing === undefined) {
      index.set(source.supplier_name, groups.length)
      groups.push({ supplier: source.supplier_name, sources: [source] })
      continue
    }
    groups[existing].sources.push(source)
  }
  return groups
})

const listCountLabel = computed(() => (
  listQuery.value.trim() === ''
    ? String(sources.value.length)
    : `${filteredSources.value.length}/${sources.value.length}`
))

const editorTitle = computed(() => {
  if (selected.value) return t('admin.supplierSources.editorEdit')
  if (copiedFrom.value) return t('admin.supplierSources.editorCopy')
  return t('admin.supplierSources.editorNew')
})

function sourceMatchesQuery(source: SupplierSource, rawQuery: string): boolean {
  const query = rawQuery.trim().toLowerCase()
  if (query === '') return true
  const haystack = [
    source.supplier_name,
    source.supplier_lane,
    source.notes,
    ...source.models.flatMap(model => [model.client_model_id, model.upstream_model_id]),
  ].join('\n').toLowerCase()
  return haystack.includes(query)
}
const syncSucceeded = computed(() => (
  syncResult.value !== null
  && syncError.value === ''
  && !syncResult.value.failed_step
))

const discoverEmptyDetail = computed(() => {
  const result = discoverResult.value
  if (!result || result.failed_step || syncError.value) return false
  if (result.probe_status === 'running' || result.probe_status === 'pending') return false
  return result.normalized_changes.length === 0
    && result.suggested_appends.length === 0
    && result.configured_issues.length === 0
    && result.rejected_candidates.length === 0
})

const emptyModel = (): SupplierSourceModel => ({
  client_model_id: '',
  upstream_model_id: '',
  purchase_ratio: null,
})

const form = reactive<SupplierSourceInput>({
  supplier_name: '',
  supplier_lane: 'default',
  channel_type: 1,
  endpoint: '',
  credential: '',
  base_priority: 100,
  account_concurrency: 1000,
  models: [emptyModel()],
  notes: '',
})

function applyChannelTypeDefaultEndpoint(channelType: number): void {
  const selected = channelTypes.value.find(item => item.channel_type === channelType)
  const baseUrl = selected?.base_url?.trim()
  if (baseUrl) {
    form.endpoint = baseUrl.replace(/\/v1\/?$/, '')
  }
}

async function load(): Promise<void> {
  loading.value = true
  try {
    await loadChannelTypes().catch(() => undefined)
    sources.value = await adminAPI.supplierSources.list()
    const requestedSourceID = sourceIDFromQuery(route.query.source_id)
    const requestedSource = requestedSourceID === null
      ? null
      : sources.value.find(source => source.id === requestedSourceID) ?? null
    if (requestedSource) selectSource(requestedSource)
  } finally {
    loading.value = false
  }
}

function sourceIDFromQuery(rawSourceID: unknown): number | null {
  const raw = Array.isArray(rawSourceID) ? rawSourceID[0] : rawSourceID
  if (typeof raw !== 'string' || !/^[1-9]\d*$/.test(raw)) return null
  const sourceID = Number(raw)
  return Number.isSafeInteger(sourceID) ? sourceID : null
}

function resetForm(): void {
  selected.value = null
  copiedFrom.value = null
  syncResult.value = null
  discoverResult.value = null
  validateResult.value = null
  discoverNeedsSave.value = false
  syncError.value = ''
  saveError.value = ''
  syncDiscoverChannelScopedForSource(null)
  Object.assign(form, {
    supplier_name: '',
    supplier_lane: 'default',
    channel_type: 1,
    endpoint: '',
    credential: '',
    base_priority: 100,
    account_concurrency: 1000,
    models: [emptyModel()],
    notes: '',
  })
}

function selectSource(source: SupplierSource): void {
  selected.value = source
  copiedFrom.value = null
  syncResult.value = null
  discoverResult.value = null
  validateResult.value = null
  discoverNeedsSave.value = false
  syncError.value = ''
  saveError.value = ''
  syncDiscoverChannelScopedForSource(source)
  Object.assign(form, {
    supplier_name: source.supplier_name,
    supplier_lane: source.supplier_lane,
    channel_type: source.channel_type,
    endpoint: source.endpoint,
    credential: '',
    base_priority: source.base_priority,
    account_concurrency: source.account_concurrency,
    models: source.models.length > 0 ? source.models.map(model => ({ ...model })) : [emptyModel()],
    notes: source.notes,
  })
}

function nextCopySupplierLane(source: Pick<SupplierSource, 'supplier_name' | 'supplier_lane' | 'endpoint'>): string {
  const suffix = t('admin.supplierSources.copySuffix')
  const taken = new Set(
    sources.value
      .filter(item => item.supplier_name === source.supplier_name && item.endpoint === source.endpoint)
      .map(item => item.supplier_lane),
  )
  const candidates = [`${source.supplier_lane} (${suffix})`]
  for (let n = 2; n < 100; n++) candidates.push(`${source.supplier_lane} (${suffix} ${n})`)
  for (const name of candidates) {
    if (!taken.has(name) && name.length <= 120) return name
  }
  return candidates[0].slice(0, 120)
}

function copySelected(): void {
  const origin = selected.value
  if (!origin) return
  const input = buildInput()
  copiedFrom.value = origin
  selected.value = null
  syncResult.value = null
  discoverResult.value = null
  validateResult.value = null
  discoverNeedsSave.value = false
  syncError.value = ''
  saveError.value = ''
  Object.assign(form, {
    supplier_name: input.supplier_name,
    supplier_lane: nextCopySupplierLane({
      supplier_name: input.supplier_name,
      supplier_lane: input.supplier_lane || origin.supplier_lane,
      endpoint: input.endpoint,
    }),
    channel_type: input.channel_type,
    endpoint: input.endpoint,
    credential: '',
    base_priority: Number.isFinite(input.base_priority) ? input.base_priority : origin.base_priority,
    account_concurrency: Number.isFinite(input.account_concurrency)
      ? input.account_concurrency
      : origin.account_concurrency,
    models: input.models.length > 0 ? input.models.map(model => ({ ...model })) : [emptyModel()],
    notes: input.notes,
  })
  syncDiscoverChannelScopedForSource({ channel_type: Number(input.channel_type) })
  void nextTick(() => {
    editorEl.value?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
    document.querySelector<HTMLInputElement>('[data-test="supplier-lane"]')?.focus()
  })
}

function addModel(): void {
  form.models.push(emptyModel())
}

function removeModel(index: number): void {
  if (form.models.length === 1) return
  form.models.splice(index, 1)
}

function discountBand(rawRatio: number | null): number {
  const ratio = typeof rawRatio === 'number' && Number.isFinite(rawRatio) ? rawRatio : null
  if (ratio === null || ratio === 1) return 6
  if (ratio < 0.2) return 1
  if (ratio < 0.4) return 2
  if (ratio < 0.6) return 3
  if (ratio < 0.8) return 4
  return 5
}

function modelPriority(ratio: number | null): number {
  const basePriority = Number.isFinite(Number(form.base_priority)) ? Number(form.base_priority) : 100
  return basePriority + discountBand(ratio) * SUPPLIER_DISCOUNT_PRIORITY_STEP
}

function buildInput(): SupplierSourceInput {
  const models = form.models
    .filter(model => model.client_model_id.trim() !== '' || model.upstream_model_id.trim() !== '')
    .map(model => ({
      client_model_id: model.client_model_id.trim(),
      upstream_model_id: model.upstream_model_id.trim(),
      purchase_ratio: typeof model.purchase_ratio === 'number' && Number.isFinite(model.purchase_ratio)
        ? model.purchase_ratio
        : null,
    }))
  return {
    supplier_name: form.supplier_name.trim(),
    supplier_lane: form.supplier_lane.trim(),
    channel_type: Number(form.channel_type),
    endpoint: form.endpoint.trim(),
    credential: form.credential,
    base_priority: Number(form.base_priority),
    account_concurrency: Number(form.account_concurrency),
    models,
    notes: form.notes.trim(),
  }
}

const hasUnsavedChanges = computed(() => {
  const source = selected.value
  if (!source) return false
  const input = buildInput()
  if (input.credential.trim() !== '') return true
  if (
    input.supplier_name !== source.supplier_name
    || input.supplier_lane !== source.supplier_lane
    || input.channel_type !== source.channel_type
    || input.endpoint !== source.endpoint
    || input.base_priority !== source.base_priority
    || input.account_concurrency !== source.account_concurrency
    || input.notes !== source.notes
  ) return true
  return JSON.stringify(input.models) !== JSON.stringify(source.models)
})

const blocksDiscoverValidateProject = computed(() => hasUnsavedChanges.value || discoverNeedsSave.value)
const canSaveSelected = computed(() => !selected.value || hasUnsavedChanges.value || discoverNeedsSave.value)

function syncDiscoverChannelScopedForSource(source: Pick<SupplierSource, 'channel_type'> | null): void {
  // Default on when a source is selected: Anthropic → Claude* via SSOT; channels
  // without a family rule still probe every probeable type.
  discoverChannelScoped.value = source != null
}

function replaceSource(source: SupplierSource): void {
  const index = sources.value.findIndex(item => item.id === source.id)
  if (index >= 0) sources.value[index] = source
  else sources.value.unshift(source)
  selectSource(source)
}

async function save(): Promise<void> {
  saving.value = true
  saveError.value = ''
  try {
    const input = buildInput()
    const source = selected.value
      ? await adminAPI.supplierSources.update(selected.value.id, input)
      : await adminAPI.supplierSources.create(input)
    replaceSource(source)
  } catch (error) {
    saveError.value = error instanceof Error ? error.message : String(error)
  } finally {
    saving.value = false
  }
}

async function loadPriorityPreview(): Promise<void> {
  previewing.value = true
  try {
    priorityPreview.value = await adminAPI.supplierSources.priorityPreview()
  } finally {
    previewing.value = false
  }
}

function applyDiscoverToForm(result: SupplierSourceProbeResult): void {
  // Draft normalized configured rows plus probe-passed suggestions into the editor
  // without saving. Operators can edit the draft, then save explicitly.
  const drafted = result.normalized_models.length > 0
    ? result.normalized_models.map(model => ({
      client_model_id: model.client_model_id,
      upstream_model_id: model.upstream_model_id,
      purchase_ratio: model.purchase_ratio,
    }))
    : []
  const existing = new Set(
    drafted.map(model => model.client_model_id.trim().toLowerCase()).filter(Boolean),
  )
  for (const model of result.suggested_appends) {
    const key = model.client_model_id.trim().toLowerCase()
    if (!key || existing.has(key)) continue
    drafted.push({
      client_model_id: model.client_model_id,
      upstream_model_id: model.upstream_model_id,
      purchase_ratio: model.purchase_ratio,
    })
    existing.add(key)
  }
  form.models = drafted.length > 0 ? drafted : [emptyModel()]
}

async function discoverSelected(): Promise<void> {
  if (!selected.value || blocksDiscoverValidateProject.value) return
  discovering.value = true
  syncResult.value = null
  discoverResult.value = null
  validateResult.value = null
  discoverNeedsSave.value = false
  syncError.value = ''
  try {
    const started = await adminAPI.supplierSources.discover(
      selected.value.id,
      discoverChannelScoped.value ? { channelScoped: true } : {},
    )
    const discovered = await waitDiscoverJob(selected.value.id, started)
    discoverResult.value = discovered
    const shouldDraft = discovered.needs_confirmation
      || discovered.normalized_changes.length > 0
      || discovered.suggested_appends.length > 0
    if (shouldDraft) {
      applyDiscoverToForm(discovered)
      discoverNeedsSave.value = true
    }
  } catch (error) {
    const discovered = supplierDiscoverResultFromError(error)
    if (discovered) {
      discoverResult.value = discovered
      const shouldDraft = discovered.needs_confirmation
        || discovered.normalized_changes.length > 0
        || discovered.suggested_appends.length > 0
      if (shouldDraft) {
        applyDiscoverToForm(discovered)
        discoverNeedsSave.value = true
      }
    }
    syncError.value = error instanceof Error ? error.message : String(error)
  } finally {
    discovering.value = false
  }
}

function supplierSyncResultFromError(error: unknown): SupplierSourceSyncResult | null {
  if (!error || typeof error !== 'object') return null
  const data = (error as { data?: unknown }).data
  if (!data || typeof data !== 'object') return null
  const candidate = data as Partial<SupplierSourceSyncResult>
  if (!Array.isArray(candidate.probe_results) || !Array.isArray(candidate.changes)) return null
  return candidate as SupplierSourceSyncResult
}

function supplierDiscoverResultFromError(error: unknown): SupplierSourceProbeResult | null {
  if (!error || typeof error !== 'object') return null
  const data = (error as { data?: unknown }).data
  if (!data || typeof data !== 'object') return null
  const candidate = data as Partial<SupplierSourceProbeResult>
  if (!Array.isArray(candidate.normalized_models) || !Array.isArray(candidate.suggested_appends)) return null
  return {
    source_id: typeof candidate.source_id === 'number' ? candidate.source_id : 0,
    job_id: typeof candidate.job_id === 'string' ? candidate.job_id : undefined,
    probe_status: candidate.probe_status === 'running'
      || candidate.probe_status === 'completed'
      || candidate.probe_status === 'failed'
      || candidate.probe_status === 'pending'
      ? candidate.probe_status
      : 'failed',
    probe_total: typeof candidate.probe_total === 'number' ? candidate.probe_total : 0,
    probe_done: typeof candidate.probe_done === 'number' ? candidate.probe_done : 0,
    upstream_models: Array.isArray(candidate.upstream_models) ? candidate.upstream_models : [],
    normalized_models: candidate.normalized_models,
    normalized_changes: Array.isArray(candidate.normalized_changes) ? candidate.normalized_changes : [],
    suggested_appends: candidate.suggested_appends,
    rejected_candidates: Array.isArray(candidate.rejected_candidates) ? candidate.rejected_candidates : [],
    configured_issues: Array.isArray(candidate.configured_issues) ? candidate.configured_issues : [],
    probe_results: Array.isArray(candidate.probe_results) ? candidate.probe_results : [],
    needs_confirmation: Boolean(candidate.needs_confirmation),
    failed_step: typeof candidate.failed_step === 'string' ? candidate.failed_step : undefined,
  }
}

function supplierValidateResultFromError(error: unknown): SupplierSourceValidateResult | null {
  if (!error || typeof error !== 'object') return null
  const data = (error as { data?: unknown }).data
  if (!data || typeof data !== 'object') return null
  const candidate = data as Partial<SupplierSourceValidateResult>
  if (!Array.isArray(candidate.probe_results)) return null
  return {
    source_id: typeof candidate.source_id === 'number' ? candidate.source_id : 0,
    probe_results: candidate.probe_results,
    failed_step: typeof candidate.failed_step === 'string' ? candidate.failed_step : undefined,
  }
}

async function waitDiscoverJob(
  sourceID: number,
  started: SupplierSourceProbeResult,
): Promise<SupplierSourceProbeResult> {
  let current = started
  discoverResult.value = current
  const jobID = current.job_id
  if (!jobID || current.probe_status !== 'running') {
    return current
  }
  const deadline = Date.now() + 15 * 60 * 1000
  while (Date.now() < deadline) {
    await new Promise(resolve => window.setTimeout(resolve, 1000))
    current = await adminAPI.supplierSources.getDiscoverJob(sourceID, jobID)
    discoverResult.value = current
    if (current.probe_status === 'completed') {
      return current
    }
    if (current.probe_status === 'failed') {
      const message = current.failed_step
        ? `${t('admin.supplierSources.failedStep')}: ${current.failed_step}`
        : t('admin.supplierSources.discoverPanelTitle')
      throw Object.assign(new Error(message), { data: current, status: 422 })
    }
  }
  throw new Error('supplier discover timed out')
}

async function validateSelected(): Promise<void> {
  if (!selected.value || blocksDiscoverValidateProject.value) return
  validating.value = true
  syncResult.value = null
  validateResult.value = null
  syncError.value = ''
  try {
    validateResult.value = await adminAPI.supplierSources.validate(selected.value.id)
  } catch (error) {
    validateResult.value = supplierValidateResultFromError(error)
    syncError.value = error instanceof Error ? error.message : String(error)
  } finally {
    validating.value = false
  }
}

async function syncSelected(): Promise<void> {
  if (!selected.value || blocksDiscoverValidateProject.value) return
  syncing.value = true
  syncResult.value = null
  syncError.value = ''
  try {
    syncResult.value = await adminAPI.supplierSources.sync(selected.value.id)
  } catch (error) {
    syncResult.value = supplierSyncResultFromError(error)
    syncError.value = error instanceof Error ? error.message : String(error)
  } finally {
    syncing.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.supplier-sources-page {
  /* Match TablePageLayout: header 64px + AppLayout lg:p-8 vertical padding. */
  height: calc(100vh - 64px - 4rem);
}

@media (max-width: 1023px) {
  .supplier-sources-page {
    height: auto;
    min-height: calc(100vh - 64px - 2rem);
  }
}
</style>
