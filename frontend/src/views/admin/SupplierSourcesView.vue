<template>
  <div class="mx-auto max-w-7xl space-y-6 p-4 sm:p-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
          {{ t('admin.supplierSources.title') }}
        </h1>
        <p class="mt-1 text-sm text-gray-500">
          {{ t('admin.supplierSources.description') }}
        </p>
      </div>
      <button
        data-test="priority-preview-button"
        type="button"
        class="rounded-lg border border-gray-300 px-4 py-2 text-sm dark:border-dark-600"
        :disabled="previewing"
        @click="loadPriorityPreview"
      >
        {{ t('admin.supplierSources.globalPriorityPreview') }}
      </button>
    </header>

    <section
      v-if="priorityPreview"
      data-test="priority-preview"
      class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800"
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
              <td class="px-2 py-2">{{ entry.supplier_name }}/{{ entry.channel_name }}</td>
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

    <div class="grid gap-6 lg:grid-cols-[320px_minmax(0,1fr)]">
      <section class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
        <div class="mb-3 flex items-center justify-between">
          <h2 class="font-medium">{{ t('admin.supplierSources.sources') }}</h2>
          <button
            data-test="new-source"
            class="text-sm text-primary-600"
            type="button"
            @click="resetForm"
          >
            {{ t('admin.supplierSources.newSource') }}
          </button>
        </div>
        <div v-if="loading" class="text-sm text-gray-500">{{ t('common.loading') }}</div>
        <div v-else-if="sources.length === 0" class="text-sm text-gray-500">
          {{ t('admin.supplierSources.empty') }}
        </div>
        <button
          v-for="source in sources"
          :key="source.id"
          :data-test="`source-select-${source.id}`"
          type="button"
          class="mb-2 w-full rounded-lg border p-3 text-left"
          :class="selected?.id === source.id
            ? 'border-primary-500 bg-primary-50 dark:bg-primary-950/20'
            : 'border-gray-200 dark:border-dark-600'"
          @click="selectSource(source)"
        >
          <div class="font-medium">{{ source.supplier_name }} · {{ source.channel_name }}</div>
          <div class="mt-1 text-xs text-gray-500">
            priority {{ source.base_priority }} · {{ source.models.length }} models
          </div>
        </button>
      </section>

      <section class="space-y-5 rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
        <form class="space-y-4" @submit.prevent="save">
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="text-sm">
              {{ t('admin.supplierSources.supplierName') }}
              <input v-model.trim="form.supplier_name" data-test="supplier-name" required class="mt-1 w-full rounded-lg border px-3 py-2" />
            </label>
            <label class="text-sm">
              {{ t('admin.supplierSources.channelName') }}
              <input v-model.trim="form.channel_name" data-test="channel-name" required class="mt-1 w-full rounded-lg border px-3 py-2" />
            </label>
          </div>

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
          <div class="flex flex-wrap gap-2">
            <button
              data-test="save-source"
              type="submit"
              :disabled="saving || syncing"
              class="rounded-lg bg-primary-600 px-4 py-2 text-white disabled:opacity-50"
            >
              {{ t('admin.supplierSources.save') }}
            </button>
            <button
              v-if="selected"
              data-test="sync-source"
              type="button"
              :disabled="saving || syncing || hasUnsavedChanges"
              class="rounded-lg border border-gray-300 px-4 py-2 disabled:opacity-50 dark:border-dark-600"
              @click="syncSelected"
            >
              {{ t('admin.supplierSources.sync') }}
            </button>
            <span
              v-if="selected && hasUnsavedChanges"
              data-test="sync-save-first"
              class="self-center text-sm text-amber-700"
            >
              {{ t('admin.supplierSources.saveBeforeSync') }}
            </span>
          </div>
        </form>

        <section
          v-if="syncResult"
          data-test="sync-result"
          class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-700"
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
          <p v-if="syncError" class="text-sm text-red-600">{{ syncError }}</p>

          <div>
            <h3 class="text-sm font-medium">{{ t('admin.supplierSources.probes') }}</h3>
            <p v-if="syncResult.probe_results.length === 0" class="mt-1 text-sm text-gray-500">
              {{ t('admin.supplierSources.noProbes') }}
            </p>
            <ul v-else class="mt-2 space-y-2 text-sm">
              <li v-for="probe in syncResult.probe_results" :key="`${probe.client_model_id}-${probe.upstream_model_id}`" class="rounded-lg bg-gray-50 p-3 dark:bg-dark-900">
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
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import {
  adminAPI,
  type SupplierPriorityPreview,
  type SupplierSource,
  type SupplierSourceInput,
  type SupplierSourceModel,
  type SupplierSourceSyncResult,
} from '@/api/admin'

const { t } = useI18n()
const route = useRoute()

const loading = ref(true)
const saving = ref(false)
const syncing = ref(false)
const previewing = ref(false)
const sources = ref<SupplierSource[]>([])
const selected = ref<SupplierSource | null>(null)
const priorityPreview = ref<SupplierPriorityPreview | null>(null)
const syncResult = ref<SupplierSourceSyncResult | null>(null)
const syncError = ref('')
const saveError = ref('')
const syncSucceeded = computed(() => (
  syncResult.value !== null
  && syncError.value === ''
  && !syncResult.value.failed_step
))

const emptyModel = (): SupplierSourceModel => ({
  client_model_id: '',
  upstream_model_id: '',
  purchase_ratio: null,
})

const form = reactive<SupplierSourceInput>({
  supplier_name: '',
  channel_name: '',
  endpoint: '',
  credential: '',
  base_priority: 100,
  models: [emptyModel()],
  notes: '',
})

async function load(): Promise<void> {
  loading.value = true
  try {
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
  syncResult.value = null
  syncError.value = ''
  saveError.value = ''
  Object.assign(form, {
    supplier_name: '',
    channel_name: '',
    endpoint: '',
    credential: '',
    base_priority: 100,
    models: [emptyModel()],
    notes: '',
  })
}

function selectSource(source: SupplierSource): void {
  selected.value = source
  syncResult.value = null
  syncError.value = ''
  saveError.value = ''
  Object.assign(form, {
    supplier_name: source.supplier_name,
    channel_name: source.channel_name,
    endpoint: source.endpoint,
    credential: '',
    base_priority: source.base_priority,
    models: source.models.length > 0 ? source.models.map(model => ({ ...model })) : [emptyModel()],
    notes: source.notes,
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
  return basePriority + discountBand(ratio)
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
    channel_name: form.channel_name.trim(),
    endpoint: form.endpoint.trim(),
    credential: form.credential,
    base_priority: Number(form.base_priority),
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
    || input.channel_name !== source.channel_name
    || input.endpoint !== source.endpoint
    || input.base_priority !== source.base_priority
    || input.notes !== source.notes
  ) return true
  return JSON.stringify(input.models) !== JSON.stringify(source.models)
})

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

function supplierSyncResultFromError(error: unknown): SupplierSourceSyncResult | null {
  if (!error || typeof error !== 'object') return null
  const data = (error as { data?: unknown }).data
  if (!data || typeof data !== 'object') return null
  const candidate = data as Partial<SupplierSourceSyncResult>
  if (!Array.isArray(candidate.probe_results) || !Array.isArray(candidate.changes)) return null
  return candidate as SupplierSourceSyncResult
}

async function syncSelected(): Promise<void> {
  if (!selected.value || hasUnsavedChanges.value) return
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
