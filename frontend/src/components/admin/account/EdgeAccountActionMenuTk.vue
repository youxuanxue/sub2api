<template>
  <Teleport to="body">
    <div v-if="show && position && account">
      <!-- Backdrop: click anywhere outside to close -->
      <div class="fixed inset-0 z-[9998]" @click="emit('close')"></div>
      <div
        class="fixed z-[9999] w-56 overflow-hidden rounded-xl bg-white shadow-lg ring-1 ring-black/5 dark:bg-dark-800"
        :style="{ top: position.top + 'px', left: position.left + 'px' }"
        @click.stop
      >
        <div class="py-1">
          <button
            v-if="canQueryUsage"
            class="flex w-full items-center gap-2 px-4 py-2 text-sm hover:bg-gray-100 dark:hover:bg-dark-700"
            @click="emit('query-usage'); emit('close')"
          >
            <Icon name="chart" size="sm" class="text-indigo-500" />
            {{ t('admin.accounts.edgePanel.queryUsage') }}
          </button>
          <button
            v-if="isRateLimited"
            class="flex w-full items-center gap-2 px-4 py-2 text-sm text-amber-600 hover:bg-gray-100 dark:hover:bg-dark-700"
            @click="emit('clear-rate-limit'); emit('close')"
          >
            <Icon name="sync" size="sm" />
            {{ t('admin.accounts.edgePanel.clearRateLimit') }}
          </button>
          <button
            v-if="isTempUnsched"
            class="flex w-full items-center gap-2 px-4 py-2 text-sm text-amber-600 hover:bg-gray-100 dark:hover:bg-dark-700"
            @click="emit('clear-temp-unschedulable'); emit('close')"
          >
            <Icon name="sync" size="sm" />
            {{ t('admin.accounts.edgePanel.clearTempUnsched') }}
          </button>
          <button
            v-if="canResetQuota"
            class="flex w-full items-center gap-2 px-4 py-2 text-sm text-teal-600 hover:bg-gray-100 dark:hover:bg-dark-700"
            @click="emit('reset-quota'); emit('close')"
          >
            <Icon name="refresh" size="sm" />
            {{ t('admin.accounts.edgePanel.resetQuota') }}
          </button>
          <div class="my-1 border-t border-gray-100 dark:border-dark-700"></div>
          <!-- Credential-class management (edit / reauth / create / delete) stays on
               the edge: this jumps to the edge's own /admin/accounts, auto-logged-in.
               Secrets never traverse prod. -->
          <button
            class="flex w-full items-center gap-2 px-4 py-2 text-sm text-blue-600 hover:bg-gray-100 dark:hover:bg-dark-700"
            @click="emit('manage'); emit('close')"
          >
            <Icon name="link" size="sm" />
            {{ t('admin.accounts.edgePanel.manageOnEdge') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { EdgeAccountSummary } from '@/api/admin/edgeAccounts'

const props = defineProps<{
  show: boolean
  account: EdgeAccountSummary | null
  position: { top: number; left: number } | null
}>()

const emit = defineEmits<{
  close: []
  'query-usage': []
  'clear-rate-limit': []
  'clear-temp-unschedulable': []
  'reset-quota': []
  manage: []
}>()

const { t } = useI18n()

function isFuture(ts?: string | null): boolean {
  return !!ts && new Date(ts).getTime() > Date.now()
}

// Usage query is meaningful for OAuth / setup-token accounts (they carry rolling
// usage windows); pure api-key accounts have none.
const canQueryUsage = computed(() => {
  const ty = props.account?.type
  return ty === 'oauth' || ty === 'setup-token'
})
const isRateLimited = computed(
  () => isFuture(props.account?.rate_limit_reset_at) || isFuture(props.account?.overload_until)
)
const isTempUnsched = computed(() => isFuture(props.account?.temp_unschedulable_until))
// Reset-quota resets the OpenAI OAuth (codex) rolling window — the case where it is
// clearly meaningful; anthropic oauth uses window-cost cleared via clear-rate-limit.
const canResetQuota = computed(
  () => props.account?.platform === 'openai' && props.account?.type === 'oauth'
)

const handleKeydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') emit('close')
}

watch(
  () => props.show,
  (visible) => {
    if (visible) window.addEventListener('keydown', handleKeydown)
    else window.removeEventListener('keydown', handleKeydown)
  },
  { immediate: true }
)

onUnmounted(() => window.removeEventListener('keydown', handleKeydown))
</script>
