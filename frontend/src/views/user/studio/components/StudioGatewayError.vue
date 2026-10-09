<template>
  <div class="rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-800 dark:border-red-900/50 dark:bg-red-950/40 dark:text-red-100">
    {{ message }}
    <router-link v-if="studioErrorShowsTopUp(code)" to="/purchase" class="ml-1 font-medium underline">{{ t('studio.topUp') }}</router-link>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { studioErrorShowsTopUp, type StudioErrorCode } from '@/utils/studioGatewayError.tk'

defineProps<{ message: string; code: StudioErrorCode | '' }>()
const { t } = useI18n()
</script>
