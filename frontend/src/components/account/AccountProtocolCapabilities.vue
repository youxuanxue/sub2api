<script setup lang="ts">
type Protocol = 'messages' | 'chat_completions' | 'responses' | 'gemini_generate_content'

defineProps<{
  protocols: Protocol[]
}>()

const labels: Record<Protocol, string> = {
  messages: 'Messages',
  chat_completions: 'Chat Completions',
  responses: 'Responses',
  gemini_generate_content: 'Gemini Generate Content'
}
</script>

<template>
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
      未检测到可用文本协议
    </span>
  </div>
</template>
