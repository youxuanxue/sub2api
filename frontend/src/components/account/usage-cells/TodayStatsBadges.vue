<template>
  <UsageStatsRow :label="t('common.today')" :stats="stats" :loading="loading" />
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import UsageStatsRow from '../UsageStatsRow.vue'

withDefaults(defineProps<{
  stats?: WindowStats | null
  loading?: boolean
}>(), {
  stats: null,
  loading: false
})

const { t } = useI18n()

</script>
