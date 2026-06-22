import { computed, type ComputedRef } from 'vue'
import { formatCompactNumber } from '@/utils/format'
import type { AccountUsageCellProps } from '../accountUsageCellProps'

export function useTodayStatsFormatters(props: AccountUsageCellProps) {
  const formatKeyRequests: ComputedRef<string> = computed(() => {
    if (!props.todayStats) return ''
    return formatCompactNumber(props.todayStats.requests, { allowBillions: false })
  })

  const formatKeyTokens: ComputedRef<string> = computed(() => {
    if (!props.todayStats) return ''
    return formatCompactNumber(props.todayStats.tokens)
  })

  const formatKeyCost: ComputedRef<string> = computed(() => {
    if (!props.todayStats) return '0.00'
    return props.todayStats.cost.toFixed(2)
  })

  const formatKeyUserCost: ComputedRef<string> = computed(() => {
    if (!props.todayStats || props.todayStats.user_cost == null) return '0.00'
    return props.todayStats.user_cost.toFixed(2)
  })

  return {
    formatKeyRequests,
    formatKeyTokens,
    formatKeyCost,
    formatKeyUserCost
  }
}
