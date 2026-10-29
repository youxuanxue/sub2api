import { computed, getCurrentInstance, onActivated, onDeactivated, onScopeDispose, ref, watch, type Ref } from 'vue'
import { getHistoricalBilling, type HistoricalBillingReport } from '@/api/historicalBilling'

// User/admin surfaces share selection, invalidation, cancellation and retries.
export function useHistoricalBilling(admin: boolean, userId: Ref<number | undefined>) {
  const expanded = ref(false)
  const range = ref('all')
  const startDate = ref('')
  const endDate = ref('')
  const granularity = ref<'day' | 'month'>('day')
  const platform = ref<string | null>(null)
  const report = ref<HistoricalBillingReport | null>(null)
  const platforms = ref<string[]>([])
  const loading = ref(false)
  const failed = ref(false)
  const invalidRange = computed(() => range.value === 'custom' &&
    (!startDate.value || !endDate.value || startDate.value > endDate.value))
  let controller: AbortController | null = null
  let revision = 0
  let active = true
  async function reload() {
    const current = ++revision
    controller?.abort()
    controller = null
    report.value = null
    failed.value = false
    loading.value = false
    if (!active || !expanded.value || (admin && !userId.value) || invalidRange.value) return
    const c = new AbortController()
    controller = c
    loading.value = true
    try {
      const result = await getHistoricalBilling({
        granularity: granularity.value,
        ...(range.value === 'custom' ? { start_date: startDate.value, end_date: endDate.value } : {}),
        ...(platform.value !== null ? { platform: platform.value } : {}),
      }, { admin, userId: userId.value, signal: c.signal })
      if (current !== revision || c.signal.aborted) return
      report.value = result
      platforms.value = result.platforms
      // Seed the custom picker using the server's billing calendar.
      if (range.value === 'all') {
        startDate.value = result.start_date
        endDate.value = result.end_date
      }
    } catch {
      if (current === revision && !c.signal.aborted) failed.value = true
    } finally {
      if (current === revision) loading.value = false
    }
  }
  watch(userId, () => { platform.value = null; platforms.value = [] }, { flush: 'sync' })
  watch([expanded, userId, range, granularity, platform,
    () => range.value === 'custom' ? startDate.value : '',
    () => range.value === 'custom' ? endDate.value : '',
  ], reload, { flush: 'sync' })
  if (getCurrentInstance()) {
    onActivated(() => { active = true; void reload() })
    onDeactivated(() => { active = false; void reload() })
  }
  onScopeDispose(() => { active = false; void reload() })
  return { expanded, range, startDate, endDate, granularity, platform, report, platforms, loading, failed, invalidRange, reload }
}
