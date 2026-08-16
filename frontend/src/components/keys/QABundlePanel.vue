<template>
  <BaseDialog
    :show="show"
    :title="t('keys.qaBundle.title')"
    width="extra-wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-3 dark:border-dark-700">
        <div class="min-w-0 text-xs text-gray-500 dark:text-gray-400">
          <template v-if="qa.job.value?.status === 'ready'">
            <div>{{ t('keys.qaBundle.window', { from: formatTime(qa.job.value.data_from), until: formatTime(qa.job.value.data_until) }) }}</div>
            <div>{{ t('keys.qaBundle.watermark', { time: formatTime(qa.job.value.archive_watermark) }) }}</div>
          </template>
        </div>
        <button
          type="button"
          class="btn btn-primary inline-flex items-center gap-2"
          :disabled="qa.job.value?.status !== 'ready' || qa.exporting.value"
          @click="qa.exportZip"
        >
          <Icon :name="qa.exporting.value ? 'refresh' : 'download'" size="sm" :class="{ 'animate-spin': qa.exporting.value }" />
          {{ qa.exporting.value ? t('keys.qaBundle.exporting') : t('keys.qaBundle.export') }}
        </button>
      </div>

      <div v-if="qa.loading.value && !qa.records.value.length" class="flex h-64 items-center justify-center text-sm text-gray-500">
        <Icon name="refresh" size="sm" class="mr-2 animate-spin" />
        {{ t('common.loading') }}
      </div>
      <div v-else-if="qa.error.value || qa.job.value?.status === 'failed'" class="flex h-64 flex-col items-center justify-center gap-3 text-sm text-gray-500">
        <Icon name="exclamationCircle" size="lg" class="text-red-500" />
        <span>{{ t('keys.qaBundle.failed') }}</span>
        <button type="button" class="btn btn-secondary" @click="qa.load">{{ t('common.retry') }}</button>
      </div>
      <div v-else-if="qa.job.value?.status === 'ready' && !qa.job.value.record_count" class="flex h-64 flex-col items-center justify-center gap-2 text-sm text-gray-500">
        <Icon name="inbox" size="lg" />
        <span>{{ t('keys.qaBundle.empty') }}</span>
      </div>
      <div v-else class="grid min-h-[28rem] gap-0 overflow-hidden border border-gray-200 dark:border-dark-700 lg:grid-cols-[minmax(18rem,0.9fr)_minmax(0,1.4fr)]">
        <section class="flex min-h-0 flex-col border-b border-gray-200 dark:border-dark-700 lg:border-b-0 lg:border-r">
          <div class="flex items-center justify-between border-b border-gray-200 px-3 py-2 text-xs text-gray-500 dark:border-dark-700">
            <span>{{ t('keys.qaBundle.recordCount', { count: qa.job.value?.record_count ?? 0 }) }}</span>
            <div class="flex items-center gap-1">
              <button
                type="button"
                class="rounded p-1 hover:bg-gray-100 disabled:opacity-30 dark:hover:bg-dark-700"
                :disabled="!qa.hasPreviousPage.value || qa.loading.value"
                :title="t('common.previous')"
                @click="qa.loadPage(qa.pageIndex.value - 1)"
              ><Icon name="chevronLeft" size="sm" /></button>
              <span class="min-w-12 text-center">{{ qa.pageIndex.value + 1 }} / {{ Math.max(qa.pages.value.length, 1) }}</span>
              <button
                type="button"
                class="rounded p-1 hover:bg-gray-100 disabled:opacity-30 dark:hover:bg-dark-700"
                :disabled="!qa.hasNextPage.value || qa.loading.value"
                :title="t('common.next')"
                @click="qa.loadPage(qa.pageIndex.value + 1)"
              ><Icon name="chevronRight" size="sm" /></button>
            </div>
          </div>
          <div class="max-h-[34rem] min-h-0 flex-1 overflow-y-auto">
            <button
              v-for="record in qa.records.value"
              :key="`${record.captured_at}/${record.request_id}`"
              type="button"
              class="block w-full border-b border-gray-100 px-3 py-2.5 text-left transition-colors hover:bg-gray-50 dark:border-dark-800 dark:hover:bg-dark-800"
              :class="qa.selected.value?.request_id === record.request_id ? 'bg-primary-50 dark:bg-primary-900/20' : ''"
              @click="qa.select(record)"
            >
              <div class="flex items-center justify-between gap-3">
                <span class="truncate text-sm font-medium text-gray-800 dark:text-gray-200">{{ record.requested_model || record.platform }}</span>
                <span :class="record.success ? 'text-green-600' : 'text-red-600'" class="shrink-0 text-xs">{{ record.status_code }}</span>
              </div>
              <div class="mt-1 flex items-center justify-between gap-3 text-xs text-gray-500">
                <span class="truncate">{{ formatTime(record.captured_at) }}</span>
                <span class="shrink-0">{{ record.duration_ms }} ms</span>
              </div>
            </button>
          </div>
        </section>

        <section class="min-w-0 bg-gray-50/60 dark:bg-dark-900/30">
          <div v-if="qa.selected.value" class="max-h-[37rem] overflow-auto p-4">
            <div class="mb-3 grid grid-cols-2 gap-x-5 gap-y-2 text-xs sm:grid-cols-4">
              <div><span class="text-gray-500">{{ t('keys.qaBundle.platform') }}</span><div class="truncate font-medium">{{ qa.selected.value.platform }}</div></div>
              <div><span class="text-gray-500">{{ t('keys.qaBundle.model') }}</span><div class="truncate font-medium">{{ qa.selected.value.requested_model }}</div></div>
              <div><span class="text-gray-500">{{ t('keys.qaBundle.tokens') }}</span><div class="font-medium">{{ qa.selected.value.input_tokens }} / {{ qa.selected.value.output_tokens }}</div></div>
              <div><span class="text-gray-500">{{ t('keys.qaBundle.latency') }}</span><div class="font-medium">{{ qa.selected.value.duration_ms }} ms</div></div>
            </div>
            <pre class="whitespace-pre-wrap break-words border-t border-gray-200 pt-3 font-mono text-xs leading-5 text-gray-700 dark:border-dark-700 dark:text-gray-300">{{ detailJSON }}</pre>
          </div>
        </section>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useTkQABundle } from '@/composables/useTkQABundle'

const props = defineProps<{ show: boolean; apiKeyId: number | null; apiKeyName?: string }>()
const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
const qa = useTkQABundle({ apiKeyId: toRef(props, 'apiKeyId'), apiKeyName: toRef(props, 'apiKeyName') })
const detailJSON = computed(() => JSON.stringify(qa.selected.value?.detail ?? {}, null, 2))

watch(() => [props.show, props.apiKeyId] as const, ([show]) => {
  if (show) {
    void qa.load()
  } else {
    qa.cancel()
  }
}, { immediate: true })

onBeforeUnmount(qa.cancel)

function formatTime(value?: string): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
</script>
