<template>
  <div v-if="stats || loading" class="mb-0.5 flex items-center">
    <div v-if="stats" class="flex flex-wrap items-center gap-1.5 text-[9px] text-gray-500 dark:text-gray-400">
      <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
        {{ formatRequests }} req
      </span>
      <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
        {{ formatTokens }}
      </span>
      <span
        class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
        :title="t('usage.accountBilled')"
      >
        A ${{ formatCost }}
      </span>
      <span
        v-if="stats.user_cost != null"
        class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
        :title="t('usage.userBilled')"
      >
        U ${{ formatUserCost }}
      </span>
    </div>
    <div v-else class="flex items-center gap-1">
      <div class="h-3 w-10 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-8 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-12 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatCompactNumber } from '@/utils/format'

const props = withDefaults(defineProps<{
  stats?: WindowStats | null
  loading?: boolean
}>(), {
  stats: null,
  loading: false
})

const { t } = useI18n()

const formatRequests = computed(() =>
  props.stats ? formatCompactNumber(props.stats.requests, { allowBillions: false }) : ''
)
const formatTokens = computed(() => (props.stats ? formatCompactNumber(props.stats.tokens) : ''))
const formatCost = computed(() => (props.stats ? props.stats.cost.toFixed(2) : '0.00'))
const formatUserCost = computed(() =>
  props.stats?.user_cost != null ? props.stats.user_cost.toFixed(2) : '0.00'
)
</script>
