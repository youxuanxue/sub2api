<template>
  <section class="card min-w-0 p-4 sm:p-6" data-testid="historical-billing">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h2 class="font-semibold text-gray-900 dark:text-gray-100">{{ t('historicalBilling.title') }}</h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('historicalBilling.description') }}</p>
      </div>
      <button class="btn btn-secondary" type="button" :aria-expanded="expanded" @click="expanded = !expanded">
        {{ t(expanded ? 'historicalBilling.close' : 'historicalBilling.open') }}
      </button>
    </div>
    <div v-if="expanded" class="mt-5 space-y-4" :aria-busy="loading">
      <p v-if="admin && !userId" class="text-sm text-amber-600">{{ t('historicalBilling.selectUser') }}</p>
      <template v-else>
        <p v-if="admin" class="text-sm font-medium">{{ t('historicalBilling.user') }} #{{ userId }}</p>
        <fieldset :disabled="exporting" class="flex min-w-0 flex-wrap items-end gap-3">
          <label class="w-full min-w-0 sm:w-auto sm:min-w-40"><span class="input-label">{{ t('historicalBilling.range') }}</span>
            <select v-model="range" :aria-label="t('historicalBilling.range')" class="input"><option value="all">{{ t('historicalBilling.all') }}</option><option value="custom">{{ t('historicalBilling.custom') }}</option></select>
          </label>
          <template v-if="range === 'custom'">
            <label class="min-w-0 max-w-full"><span class="input-label">{{ t('historicalBilling.start') }}</span><input v-model="startDate" class="input" type="date" :max="endDate" /></label>
            <label class="min-w-0 max-w-full"><span class="input-label">{{ t('historicalBilling.end') }}</span><input v-model="endDate" class="input" type="date" :min="startDate" /></label>
          </template>
          <label class="w-full min-w-0 sm:w-auto sm:min-w-40" for="historical-billing-platform"><span class="input-label">{{ t('historicalBilling.platform') }}</span>
            <select id="historical-billing-platform" v-model="platform" :aria-label="t('historicalBilling.platform')" class="input"><option :value="null">{{ t('historicalBilling.allPlatforms') }}</option><option v-for="item in platforms" :key="item" :value="item">{{ item || t('historicalBilling.unknownPlatform') }}</option></select>
          </label>
          <label class="min-w-0 max-w-full" for="historical-billing-granularity"><span class="input-label">{{ t('historicalBilling.granularity') }}</span>
            <select id="historical-billing-granularity" v-model="granularity" :aria-label="t('historicalBilling.granularity')" class="input"><option value="day">{{ t('historicalBilling.day') }}</option><option value="month">{{ t('historicalBilling.month') }}</option></select>
          </label>
          <button type="button" class="btn btn-secondary" :disabled="loading || invalidRange" @click="reload">{{ t('historicalBilling.refresh') }}</button>
        </fieldset>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('historicalBilling.note') }}</p>
        <p v-if="invalidRange" role="alert" class="text-sm text-amber-600">{{ t('historicalBilling.invalidRange') }}</p>
        <p v-else-if="failed" role="alert" class="text-sm text-red-600">{{ t('historicalBilling.error') }}</p>
        <p v-else-if="loading" role="status" class="text-sm text-gray-500">{{ t('historicalBilling.loading') }}</p>
        <template v-if="report">
          <div class="flex flex-wrap gap-x-6 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
            <span>{{ t('historicalBilling.scope') }}: {{ report.start_date }} → {{ report.end_date }}</span>
            <span>{{ t('historicalBilling.timezone') }}: {{ report.timezone }}</span>
            <span>{{ t('historicalBilling.coverage') }}: {{ report.available_start_date || '—' }} → {{ report.available_end_date || '—' }}</span>
            <span>{{ t('historicalBilling.queried') }}: {{ formatTime(report.generated_at) }}</span>
            <span v-if="report.rows.length">{{ t('historicalBilling.updated') }}: {{ formatTime(report.totals.computed_at) }}</span>
          </div>
          <div class="flex flex-wrap items-center gap-3">
            <button type="button" class="btn btn-secondary" :disabled="exporting || !report.rows.length" @click="download('csv')">{{ t('historicalBilling.csv') }}</button>
            <button type="button" class="btn btn-secondary" :disabled="exporting || !report.rows.length" @click="download('xlsx')">{{ t('historicalBilling.excel') }}</button>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('historicalBilling.precision') }}</span>
          </div>
          <p v-if="!report.rows.length" class="py-4 text-sm text-gray-500">{{ t('historicalBilling.empty') }}</p>
          <div v-else class="max-h-[560px] overflow-auto rounded-lg border border-gray-200 dark:border-dark-600">
            <table class="w-full whitespace-nowrap text-right text-sm tabular-nums">
              <thead class="sticky top-0 bg-gray-50 dark:bg-dark-800"><tr><th v-for="header in tableHeaders" :key="header" class="px-3 py-3">{{ header }}</th></tr></thead>
              <tbody><tr v-for="row in report.rows" :key="`${row.date}:${row.platform}`" class="border-t border-gray-100 dark:border-dark-700">
                <td class="px-3 py-2">{{ row.date }}</td><td class="px-3 py-2">{{ row.platform || t('historicalBilling.unknownPlatform') }}</td>
                <td v-for="key in counterKeys" :key="key" class="px-3 py-2">{{ row[key].toLocaleString() }}</td>
                <td class="px-3 py-2 font-medium text-emerald-600">{{ row.actual_cost }}</td>
                <td class="px-3 py-2" :class="row.provisional ? 'text-amber-600' : 'text-gray-500'">{{ t(row.provisional ? 'historicalBilling.provisional' : 'historicalBilling.recorded') }}</td>
                <td class="px-3 py-2 text-xs text-gray-500">{{ formatTime(row.computed_at) }}</td>
              </tr></tbody>
              <tfoot class="sticky bottom-0 bg-gray-50 font-semibold dark:bg-dark-800"><tr>
                <td colspan="2" class="px-3 py-3">{{ t('historicalBilling.total') }}</td>
                <td v-for="key in counterKeys" :key="key" class="px-3 py-3">{{ report.totals[key].toLocaleString() }}</td>
                <td class="px-3 py-3 text-emerald-600">{{ report.totals.actual_cost }}</td>
                <td colspan="2" class="px-3 py-3">{{ report.totals.provisional ? t('historicalBilling.provisional') : '' }}</td>
              </tr></tfoot>
            </table>
          </div>
        </template>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import { useHistoricalBilling } from '@/composables/useHistoricalBilling'
import { historicalBillingCSV, fillHistoricalBillingWorkbook } from '@/utils/historicalBillingExport'
import { useAppStore } from '@/stores/app'

const props = withDefaults(defineProps<{ admin?: boolean; userId?: number }>(), { admin: false })
const { t } = useI18n()
const app = useAppStore()
const { expanded, range, startDate, endDate, granularity, platform, report, platforms, loading, failed, invalidRange, reload } = useHistoricalBilling(props.admin, toRef(props, 'userId'))
const exporting = ref(false)
const counterKeys = ['requests', 'input_tokens', 'output_tokens', 'cache_creation_tokens', 'cache_read_tokens', 'total_tokens'] as const
const headerKeys = ['date', 'platform', 'requests', 'input', 'output', 'creation', 'read', 'tokens', 'cost']
const tableHeaders = computed(() => [...headerKeys, 'status', 'updated'].map(key => t(`historicalBilling.headers.${key}`)))
function formatTime(value: string) {
  return new Date(value).toLocaleString(undefined, { timeZone: report.value?.timezone })
}
async function download(format: 'csv' | 'xlsx') {
  const snapshot = report.value
  if (!snapshot?.rows.length || exporting.value) return
  exporting.value = true
  const labels = {
    headers: [t('historicalBilling.user'), ...headerKeys.map(key => t(`historicalBilling.headers.${key}`)), t('historicalBilling.timezone')],
    total: t('historicalBilling.total'), unknownPlatform: t('historicalBilling.unknownPlatform'),
  }
  try {
    let blob: Blob
    if (format === 'csv') blob = new Blob([historicalBillingCSV(snapshot, labels)], { type: 'text/csv;charset=utf-8' })
    else {
      const { Workbook } = await import('exceljs')
      const wb = new Workbook()
      fillHistoricalBillingWorkbook(wb, snapshot, labels)
      const buffer = await wb.xlsx.writeBuffer()
      blob = new Blob([new Uint8Array(buffer)], { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' })
    }
    if (report.value !== snapshot) return
    saveAs(blob, `billing_user-${snapshot.user_id}_${snapshot.start_date}_${snapshot.end_date}_${snapshot.granularity}.${format}`)
  } catch {
    app.showError(t('historicalBilling.exportError'))
  } finally { exporting.value = false }
}
</script>
