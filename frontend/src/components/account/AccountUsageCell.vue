<template>
  <component v-if="activeCell" :is="activeCell" v-bind="props" />
  <div v-else class="text-xs text-gray-400">-</div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  accountUsageCellPropDefaults,
  type AccountUsageCellProps
} from './accountUsageCellProps'
import { showUsageWindowsForAccount } from './usage-cells/useAccountUsageFetch'
import PlainUsageCell from './usage-cells/PlainUsageCell.vue'
import AnthropicUsageCell from './usage-cells/AnthropicUsageCell.vue'
import OpenAIUsageCell from './usage-cells/OpenAIUsageCell.vue'
import AntigravityUsageCell from './usage-cells/AntigravityUsageCell.vue'
import GeminiUsageCell from './usage-cells/GeminiUsageCell.vue'
import KiroUsageCell from './usage-cells/KiroUsageCell.vue'

const props = withDefaults(defineProps<AccountUsageCellProps>(), accountUsageCellPropDefaults)

const activeCell = computed(() => {
  const { account } = props

  if (!showUsageWindowsForAccount(account)) {
    return PlainUsageCell
  }

  if (
    account.platform === 'anthropic' &&
    (account.type === 'oauth' || account.type === 'setup-token')
  ) {
    return AnthropicUsageCell
  }

  if (
    (account.platform === 'openai' && account.type === 'oauth') ||
    account.platform === 'grok'
  ) {
    return OpenAIUsageCell
  }

  if (account.platform === 'antigravity' && account.type === 'oauth') {
    return AntigravityUsageCell
  }

  if (account.platform === 'gemini') {
    return GeminiUsageCell
  }

  if (account.platform === 'kiro') {
    return KiroUsageCell
  }

  return null
})
</script>
