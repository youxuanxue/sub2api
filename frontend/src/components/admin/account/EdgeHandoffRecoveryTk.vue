<template>
  <div v-if="edgeId" role="alert" class="my-3 flex flex-wrap items-center gap-3 rounded-lg bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-100">
    <span>{{ t('admin.edgeAccounts.handoff.failed') }}</span>
    <button type="button" class="btn btn-secondary" @click="$emit('retry')">{{ t('admin.edgeAccounts.handoff.retry') }}</button>
    <a v-if="loginUrl" :href="loginUrl" target="_blank" rel="noopener noreferrer" class="font-medium underline">{{ t('admin.edgeAccounts.handoff.goLogin') }}</a>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
defineProps<{ edgeId: string | null; loginUrl: string }>()
defineEmits<{ retry: [] }>()
const { t } = useI18n()
</script>
