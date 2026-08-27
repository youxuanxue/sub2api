<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { NativeTextProtocol, ProtocolEndpointCapabilitySummary } from '@/types'
import type { ProtocolProbeOutcome } from '@/api/admin/accounts'
import { formatDateTimeToMinute } from '@/utils/format'

defineProps<{
  protocols: NativeTextProtocol[]
  capability?: ProtocolEndpointCapabilitySummary | null
  probeOutcome?: ProtocolProbeOutcome | 'error' | null
}>()

const { t } = useI18n()

const labels: Record<NativeTextProtocol, string> = {
  messages: 'Messages',
  chat_completions: 'Chat Completions',
  responses: 'Responses',
  gemini_generate_content: 'Gemini Generate Content'
}
</script>

<template>
  <div class="space-y-1">
    <div class="flex flex-wrap items-center gap-1">
      <span
        v-for="protocol in protocols"
        :key="protocol"
        :data-protocol="protocol"
        class="inline-flex rounded bg-blue-50 px-1.5 py-0.5 text-[10px] font-medium text-blue-700 dark:bg-blue-950 dark:text-blue-200"
      >
        {{ labels[protocol] }}
      </span>
      <span v-if="protocols.length === 0" class="text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.accounts.protocolCapabilityEmpty') }}
      </span>
    </div>
    <div v-if="capability" class="flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-gray-500 dark:text-gray-400">
      <span data-testid="protocol-shared-account-count">
        {{ t('admin.accounts.protocolSharedAccounts', { count: capability.affected_account_count }) }}
      </span>
      <span
        v-if="capability.last_probed_at"
        data-testid="protocol-last-probed-at"
        :data-last-probed-at="capability.last_probed_at"
      >
        {{ t('admin.accounts.protocolLastProbedAt', { time: formatDateTimeToMinute(capability.last_probed_at) }) }}
      </span>
    </div>
    <p
      v-if="probeOutcome === 'inconclusive'"
      data-testid="protocol-probe-inconclusive"
      class="text-xs text-amber-600 dark:text-amber-400"
    >
      {{ t('admin.accounts.protocolProbeInconclusive') }}
    </p>
    <p
      v-if="probeOutcome === 'error'"
      data-testid="protocol-probe-error"
      class="text-xs text-red-600 dark:text-red-400"
    >
      {{ t('admin.accounts.protocolProbeFailed') }}
    </p>
    <p
      v-if="capability?.identity_conflict"
      data-testid="protocol-capability-conflict"
      class="text-xs text-red-600 dark:text-red-400"
    >
      {{ t('admin.accounts.protocolCapabilityConflict') }}
    </p>
  </div>
</template>
