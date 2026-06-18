<template>
  <BaseDialog
    :show="show"
    :title="t('keys.exportPanel.title')"
    width="normal"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <!-- Header: last export time + Export now action -->
      <div class="flex items-center justify-between gap-3 flex-wrap">
        <p class="text-sm text-gray-600 dark:text-gray-400">
          <template v-if="lastExportAt">
            {{ t('keys.exportPanel.lastExport', { time: formatTime(lastExportAt) }) }}
          </template>
          <template v-else>
            {{ t('keys.exportPanel.noExports') }}
          </template>
        </p>
        <button
          @click="onExportNow"
          :disabled="tkRunning || apiKeyId === null"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary-600 text-white hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-60 transition-colors"
        >
          <Icon :name="tkRunning ? 'refresh' : 'download'" size="sm" :class="{ 'animate-spin': tkRunning }" />
          <span>{{ tkRunning ? t('keys.exportPanel.exporting') : t('keys.exportPanel.exportNow') }}</span>
        </button>
      </div>

      <!-- Export list -->
      <div v-if="tkLoading" class="py-8 text-center text-sm text-gray-400">
        <Icon name="refresh" size="sm" class="animate-spin inline-block mr-1.5" />
        {{ t('common.loading') }}
      </div>
      <div
        v-else-if="!tkExports.length"
        class="flex flex-col items-center gap-2 py-8 text-center text-sm text-gray-400 dark:text-gray-500"
      >
        <Icon name="inbox" size="lg" class="text-gray-300 dark:text-dark-600" />
        <span>{{ t('keys.exportPanel.noExports') }}</span>
      </div>
      <ul v-else class="space-y-2">
        <li
          v-for="job in tkExports"
          :key="job.job_id"
          class="flex items-center justify-between gap-3 rounded-xl border border-gray-200 dark:border-dark-700 bg-gray-50/60 dark:bg-dark-800/40 px-3 py-2.5"
        >
          <div class="min-w-0 flex-1 space-y-0.5">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-sm font-medium text-gray-800 dark:text-gray-200">{{ formatTime(job.created_at) }}</span>
              <span
                class="px-1.5 py-0.5 rounded text-xs font-medium"
                :class="job.kind === 'auto'
                  ? 'bg-gray-200 dark:bg-dark-700 text-gray-600 dark:text-gray-300'
                  : 'bg-indigo-100 dark:bg-indigo-900/30 text-indigo-600 dark:text-indigo-300'"
              >{{ job.kind === 'auto' ? t('keys.exportPanel.kindAuto') : t('keys.exportPanel.kindManual') }}</span>
            </div>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('keys.exportPanel.recordCount', { count: job.record_count }) }} ·
              <span :class="statusClass(job)">{{ statusLabel(job) }}</span>
              <template v-if="job.status === 'failed' && job.error">
                — {{ errorLabel(job.error) }}
              </template>
            </p>
          </div>
          <button
            v-if="job.status === 'done' && job.download_url"
            @click="onDownload(job)"
            class="inline-flex items-center gap-1.5 shrink-0 px-2.5 py-1.5 rounded-lg text-sm font-medium text-primary-600 hover:bg-primary-50 dark:text-primary-400 dark:hover:bg-primary-900/20 transition-colors"
          >
            <Icon name="download" size="sm" />
            {{ t('keys.exportPanel.download') }}
          </button>
          <span
            v-else-if="job.status === 'done'"
            class="shrink-0 text-xs text-gray-400 dark:text-gray-500"
          >{{ t('keys.exportPanel.expired') }}</span>
        </li>
      </ul>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button @click="emit('close')" class="btn btn-secondary">
          {{ t('keys.exportPanel.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useTkExportPanel } from '@/composables/useTkExportPanel'
import type { TrajExportJob } from '@/api/qaTraj'

interface Props {
  show: boolean
  apiKeyId: number | null
  apiKeyName?: string
}

interface Emits {
  (e: 'close'): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const { t } = useI18n()

const tk = useTkExportPanel({
  apiKeyId: toRef(props, 'apiKeyId'),
  apiKeyName: toRef(props, 'apiKeyName'),
})

// Template-facing refs (top-level so they auto-unwrap in the template).
const tkExports = tk.exports
const tkLoading = tk.loading
const tkRunning = tk.running

// Newest job's enqueue time → the "last export" line. The list is newest-first.
const lastExportAt = computed(() => tkExports.value[0]?.created_at)

// Load the key's recent exports whenever the panel opens for a key.
watch(
  () => [props.show, props.apiKeyId] as const,
  ([show]) => {
    if (show) void tk.refresh()
  },
  { immediate: true },
)

function onExportNow(): void {
  void tk.exportNow()
}

function onDownload(job: TrajExportJob): void {
  void tk.download(job)
}

function formatTime(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

function statusLabel(job: TrajExportJob): string {
  return t(`keys.exportPanel.statusValue.${job.status}`)
}

function statusClass(job: TrajExportJob): string {
  switch (job.status) {
    case 'done':
      return 'text-green-600 dark:text-green-400'
    case 'failed':
      return 'text-red-600 dark:text-red-400'
    default:
      return 'text-amber-600 dark:text-amber-400'
  }
}

// Map the backend's terminal error codes to the existing export toast copy so
// the messaging stays consistent with the inline flow this panel replaces.
function errorLabel(code: string): string {
  return code === 'no_records' ? t('keys.exportEmpty') : t('keys.exportFailed')
}
</script>
