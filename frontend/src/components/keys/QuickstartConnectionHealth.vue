<template>
  <div
    data-tk="quickstart-connection-health"
    :class="rootClass"
  >
    <template v-if="layout === 'inline'">
      <div class="flex min-w-0 flex-1 flex-wrap items-center gap-2 sm:gap-3">
        <div class="flex shrink-0 flex-wrap gap-2">
          <button
            v-if="showTestButton"
            type="button"
            data-tk="quickstart-send-test"
            class="btn btn-primary inline-flex items-center gap-1.5 text-sm"
            :disabled="testDisabled"
            @click="emit('runTest')"
          >
            <Icon v-if="testState?.status === 'running'" name="refresh" size="sm" class="animate-spin" />
            <span>{{ testButtonLabel }}</span>
          </button>
          <button
            v-if="showChangeKey"
            type="button"
            data-tk="quickstart-change-key"
            class="btn btn-secondary text-sm"
            @click="emit('changeKey')"
          >
            {{ t('quickstart.changeKey') }}
          </button>
        </div>

        <div class="ml-auto flex min-w-0 items-center gap-2">
          <span
            class="inline-flex h-2 w-2 shrink-0 rounded-full"
            :class="dotClass"
            aria-hidden="true"
          />
          <p class="truncate text-sm font-medium" :class="titleClass">
            {{ statusTitle }}
            <span v-if="statusDetail" class="font-normal" :class="detailClass">
              · {{ statusDetail }}
            </span>
          </p>
        </div>
      </div>
    </template>

    <template v-else>
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex min-w-0 items-start gap-2.5">
          <span
            class="mt-0.5 inline-flex h-2.5 w-2.5 shrink-0 rounded-full"
            :class="dotClass"
            aria-hidden="true"
          />
          <div class="min-w-0">
            <p class="text-sm font-medium" :class="titleClass">
              {{ statusTitle }}
            </p>
            <p v-if="statusDetail" class="mt-0.5 text-xs" :class="detailClass">
              {{ statusDetail }}
            </p>
          </div>
        </div>

        <div class="flex shrink-0 flex-wrap gap-2">
          <button
            v-if="showTestButton"
            type="button"
            data-tk="quickstart-send-test"
            class="btn btn-primary inline-flex items-center gap-1.5 text-sm"
            :disabled="testDisabled"
            @click="emit('runTest')"
          >
            <Icon v-if="testState?.status === 'running'" name="refresh" size="sm" class="animate-spin" />
            <span>{{ testButtonLabel }}</span>
          </button>
          <button
            v-if="showChangeKey"
            type="button"
            data-tk="quickstart-change-key"
            class="btn btn-secondary text-sm"
            @click="emit('changeKey')"
          >
            {{ t('quickstart.changeKey') }}
          </button>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { TestState } from '@/composables/useTkUseKey'
import { formatProbeLatencyDetail } from '@/composables/useTkUseKey'

const props = withDefaults(defineProps<{
  testState: TestState | null
  setupBlocked?: boolean
  setupBlockedReason?: string
  /** inline: same row as transport/protocol picker; banner: standalone panel */
  layout?: 'inline' | 'banner'
}>(), {
  layout: 'banner',
})

const emit = defineEmits<{
  runTest: []
  changeKey: []
}>()

const { t } = useI18n()

const effectiveStatus = computed(() => {
  if (props.setupBlocked) return 'blocked' as const
  return props.testState?.status ?? 'idle'
})

const statusTitle = computed(() => {
  if (props.setupBlocked) return t('quickstart.connectionBlocked')
  switch (effectiveStatus.value) {
    case 'running':
      return t('quickstart.connectionTesting')
    case 'ok':
      return t('quickstart.connectionConnected')
    case 'error':
      return t('quickstart.connectionFailed')
    default:
      return t('quickstart.connectionWaiting')
  }
})

const statusDetail = computed(() => {
  if (props.setupBlocked) return props.setupBlockedReason ?? ''
  if (effectiveStatus.value === 'ok' && props.testState) {
    return formatProbeLatencyDetail(props.testState, t)
  }
  if (effectiveStatus.value === 'error') {
    if (props.testState?.reason === 'missing_tool_call') return t('quickstart.toolCallMissing')
    return props.testState?.message ?? ''
  }
  if (effectiveStatus.value === 'idle') return t('quickstart.connectionWaitingHint')
  return ''
})

const rootClass = computed(() => {
  if (props.layout === 'inline') {
    return 'min-w-0 flex-1'
  }
  const panel = 'rounded-lg border px-4 py-3'
  if (props.setupBlocked) {
    return `${panel} border-amber-200 bg-amber-50 dark:border-amber-800 dark:bg-amber-900/20`
  }
  switch (effectiveStatus.value) {
    case 'ok':
      return `${panel} border-emerald-200 bg-emerald-50 dark:border-emerald-800 dark:bg-emerald-900/20`
    case 'error':
      return `${panel} border-red-200 bg-red-50 dark:border-red-800 dark:bg-red-900/20`
    case 'running':
      return `${panel} border-primary-200 bg-primary-50 dark:border-primary-800 dark:bg-primary-900/20`
    default:
      return `${panel} border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-800/50`
  }
})

const dotClass = computed(() => {
  if (props.setupBlocked) return 'bg-amber-500'
  switch (effectiveStatus.value) {
    case 'ok':
      return 'bg-emerald-500'
    case 'error':
      return 'bg-red-500'
    case 'running':
      return 'bg-primary-500 animate-pulse'
    default:
      return 'bg-gray-400 dark:bg-gray-500'
  }
})

const titleClass = computed(() => {
  if (props.setupBlocked) return 'text-amber-900 dark:text-amber-100'
  switch (effectiveStatus.value) {
    case 'ok':
      return 'text-emerald-900 dark:text-emerald-100'
    case 'error':
      return 'text-red-900 dark:text-red-100'
    case 'running':
      return 'text-primary-900 dark:text-primary-100'
    default:
      return 'text-gray-800 dark:text-gray-100'
  }
})

const detailClass = computed(() => {
  if (props.setupBlocked) return 'text-amber-800 dark:text-amber-200'
  switch (effectiveStatus.value) {
    case 'ok':
      return 'text-emerald-800 dark:text-emerald-200'
    case 'error':
      return 'text-red-800 dark:text-red-200'
    case 'running':
      return 'text-primary-800 dark:text-primary-200'
    default:
      return 'text-gray-600 dark:text-gray-300'
  }
})

const showTestButton = computed(() => !props.setupBlocked)
const testDisabled = computed(() => effectiveStatus.value === 'running')
const showChangeKey = computed(() => props.setupBlocked || effectiveStatus.value === 'error')

const testButtonLabel = computed(() => {
  if (effectiveStatus.value === 'running') return t('quickstart.connectionTesting')
  if (effectiveStatus.value === 'error') return t('quickstart.retryTest')
  return t('quickstart.sendTest')
})
</script>
