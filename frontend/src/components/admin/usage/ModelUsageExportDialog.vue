<template>
  <BaseDialog :show="true" :title="t('usage.modelExport.title')" @close="close">
    <form id="model-export-form" class="space-y-4" :aria-busy="busy" @submit.prevent="download">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('usage.modelExport.description') }}</p>
      <p class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">{{ scope }}</p>
      <p v-if="!hasUser" class="text-sm text-amber-600">{{ t('usage.modelExport.selectUser') }}</p>
      <label class="block">
        <span class="input-label">{{ t('usage.modelExport.currency') }}</span>
        <select v-model="currency" class="input" :disabled="busy">
          <option value="USD">USD</option><option value="CNY">CNY</option>
        </select>
      </label>
      <label v-if="currency === 'CNY'" class="block">
        <span class="input-label">{{ t('usage.modelExport.rate') }}</span>
        <input v-model.number="exchangeRate" class="input" type="number" min="0.000001" step="any" required :disabled="busy" />
      </label>
      <p class="text-xs text-gray-500">{{ t('usage.modelExport.note') }}</p>
    </form>
    <template #footer>
      <button class="btn btn-secondary" type="button" @click="close">{{ t('common.cancel') }}</button>
      <button class="btn btn-primary" type="submit" form="model-export-form" :disabled="busy || !hasUser || !validRate">
        {{ busy ? t('usage.exporting') : t('usage.exportExcel') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getModelStats, type ModelStatsParams } from '@/api/admin/dashboard'
import { useAppStore } from '@/stores/app'
import { fillModelUsageWorkbook } from '@/utils/usageModelExport'

const props = defineProps<{ params: ModelStatsParams }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const app = useAppStore()
const hasUser = computed(() => Number.isSafeInteger(props.params.user_id) && Number(props.params.user_id) > 0)
const currency = ref('USD')
const exchangeRate = ref<number | ''>('')
const busy = ref(false)
let controller: AbortController | null = null
const rate = computed(() => currency.value === 'USD' ? 1 : Number(exchangeRate.value))
const validRate = computed(() => Number.isFinite(rate.value) && rate.value > 0)
const scope = computed(() => {
  const p = props.params
  const start = p.start_ts != null ? new Date(p.start_ts).toISOString() : p.start_date
  const end = p.end_ts != null ? new Date(p.end_ts).toISOString() : p.end_date
  const filterLabels: Record<string, string> = { model: t('usage.model'), api_key_id: t('usage.apiKeyFilter'), account_id: t('admin.usage.account'), group_id: t('admin.usage.group'), request_type: t('usage.type'), stream: t('usage.stream'), native_compaction_v2: t('usage.compactionFilter'), billing_type: t('admin.usage.billingType'), billing_mode: t('admin.usage.billingMode'), upstream_model_mismatch: t('usage.upstreamModelMismatch') }
  const filters = Object.entries(filterLabels).flatMap(([key, label]) => {
    const value = p[key as keyof ModelStatsParams]
    if (value == null || value === '') return []
    return [`${label}: ${typeof value === 'boolean' ? t(value ? 'common.yes' : 'common.no') : value}`]
  })
  return `#${p.user_id ?? '—'} · ${start} → ${end}${filters.length ? ' · ' + filters.join(', ') : ''}`
})
function close() { controller?.abort(); emit('close') }
onUnmounted(() => controller?.abort())
async function download() {
  if (busy.value || !hasUser.value || !validRate.value) return
  const params = { ...props.params }
  const exportScope = scope.value
  const exportRate = rate.value
  const exportCurrency = currency.value
  const c = new AbortController(); controller = c
  busy.value = true
  try {
    const { models } = await getModelStats(params, { signal: c.signal })
    if (c.signal.aborted) return
    if (!models.length) { app.showWarning(t('usage.noDataToExport')); return }
    const { Workbook } = await import('exceljs')
    if (c.signal.aborted) return
    const wb = new Workbook()
    fillModelUsageWorkbook(wb, models, exportRate, {
      title: t('usage.modelExport.title'), scope: exportScope, note: t('usage.modelExport.note'),
      rate: `${t('usage.modelExport.rate')} (${exportCurrency})`, total: t('usage.modelExport.total'),
      headers: [t('usage.modelExport.index'), t('usage.model'), 'Token',
        `${t('usage.modelExport.unitPrice')} (${exportCurrency}/Token)`,
        `${t('usage.modelExport.subtotal')} (${exportCurrency})`, t('usage.totalRequests'), `${t('usage.userBilled')} (USD)`],
    })
    const buffer = await wb.xlsx.writeBuffer()
    if (c.signal.aborted) return
    saveAs(new Blob([new Uint8Array(buffer)], { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' }),
      `usage_models_user-${params.user_id}_${params.start_date}_${params.end_date}_${exportCurrency}.xlsx`)
    app.showSuccess(t('usage.exportSuccess'))
    emit('close')
  } catch (error) {
    if (!c.signal.aborted) { console.error('Model usage export failed:', error); app.showError(t('usage.exportFailed')) }
  } finally {
    if (controller === c) { controller = null; busy.value = false }
  }
}
</script>
