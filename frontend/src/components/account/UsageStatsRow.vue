<template>
  <div v-if="stats || loading" class="flex flex-wrap items-center gap-1 text-[10px] text-gray-500 dark:text-gray-400" data-testid="usage-stats-row">
    <span class="shrink-0 font-medium">{{ label }}</span>
    <template v-if="stats">
      <span>{{ formatCompactNumber(stats.requests, { allowBillions: false }) }} req</span>
      <span>{{ formatCompactNumber(stats.tokens) }} tok</span>
      <span :title="t('usage.accountBilled')">A ${{ stats.cost.toFixed(2) }}</span>
      <span v-if="stats.user_cost != null" :title="t('usage.userBilled')">U ${{ stats.user_cost.toFixed(2) }}</span>
    </template>
    <span v-else class="h-3 w-24 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></span>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatCompactNumber } from '@/utils/format'

defineProps<{
  label: string
  stats?: WindowStats | null
  loading?: boolean
}>()
const { t } = useI18n()
</script>
