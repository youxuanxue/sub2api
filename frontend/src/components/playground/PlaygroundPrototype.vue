<!--
  PlaygroundPrototype.vue — US-032 P1-B prototype A 件 (no router/sidebar wiring).
  Spec: docs/approved/user-cold-start.md §11; .testing/user-stories/stories/US-032-playground-prototype-AB.md.
  Sibling B 件 = docs/approved/attachments/playground-prototype-2026-04-23.html
  (canonical source for the 5 design decisions in its <aside class="design-block">).
-->
<script setup lang="ts">
import { computed } from 'vue'

export type PlaygroundState = 'empty' | 'typing' | 'responded' | 'error'

interface Props {
  state: PlaygroundState
}

const props = defineProps<Props>()

const isEmpty = computed(() => props.state === 'empty')
const isTyping = computed(() => props.state === 'typing')
const isResponded = computed(() => props.state === 'responded')
const isError = computed(() => props.state === 'error')

// Hard-coded fixtures kept identical to the static HTML mockup. Any change
// here MUST be mirrored in docs/approved/attachments/playground-prototype-2026-04-23.html
// and the parity table (US-032 AC-004).
const FIXTURE = {
  groupName: 'claude-pool-default',
  modelId: 'claude-sonnet-4.5',
  inputPlaceholder: 'Ask anything — uses your trial API key',
  trialBalance: '$1.00 USD',
  userMessage: 'Write a haiku about TokenKey.',
  assistantMessage:
    'Tokens flow softly,\nKeys unlock the model paths,\nQuotas guard the gate.',
  usageInputTokens: 12,
  usageOutputTokens: 31,
  estimatedCost: '$0.000165',
  errorTitle: 'Trial balance exhausted',
  errorBody:
    'Your $1.00 trial credit is used up. Top up via Subscriptions or wait for the next reset.',
  systemPromptHint: 'System prompt — coming in v2'
} as const
</script>

<template>
  <section
    class="playground-prototype rounded-xl border border-gray-200 bg-white p-6 shadow-sm"
    :data-state="state"
    data-testid="playground-root"
  >
    <!-- Top bar: group + model selectors (visual only) -->
    <header class="flex items-center justify-between gap-4 border-b pb-4">
      <div class="flex items-center gap-2 text-sm">
        <span class="font-semibold text-gray-700">Group</span>
        <span
          class="rounded-md bg-gray-100 px-2 py-1 font-mono text-xs"
          data-testid="group-pill"
        >{{ FIXTURE.groupName }}</span>
        <span class="font-semibold text-gray-700">Model</span>
        <span
          class="rounded-md bg-gray-100 px-2 py-1 font-mono text-xs"
          data-testid="model-pill"
        >{{ FIXTURE.modelId }}</span>
      </div>
      <span class="text-xs text-gray-500" data-testid="trial-balance">
        Trial credit: {{ FIXTURE.trialBalance }}
      </span>
    </header>

    <!-- Conversation surface -->
    <div class="mt-4 min-h-[260px]">
      <!-- empty state ---------------------------------------------------- -->
      <div
        v-if="isEmpty"
        class="flex h-[260px] flex-col items-center justify-center text-center"
        data-testid="placeholder"
      >
        <div class="mb-3 text-4xl text-gray-300" aria-hidden="true">▭</div>
        <p class="text-sm text-gray-500">
          Start a conversation to see how your trial key performs.
        </p>
        <p class="mt-1 text-xs text-gray-400">
          Up to 50 turns · 4096 max tokens · 60s timeout
        </p>
      </div>

      <!-- typing state --------------------------------------------------- -->
      <div v-else class="space-y-4" data-testid="conversation">
        <!-- user turn (always present in non-empty states) -->
        <div class="flex justify-end" data-testid="user-message">
          <div
            class="max-w-[80%] rounded-2xl rounded-tr-sm bg-blue-600 px-4 py-2 text-sm text-white"
          >
            {{ FIXTURE.userMessage }}
          </div>
        </div>

        <!-- assistant turn — typing skeleton -->
        <div
          v-if="isTyping"
          class="flex justify-start"
          data-testid="assistant-typing"
        >
          <div
            class="max-w-[80%] rounded-2xl rounded-tl-sm bg-gray-100 px-4 py-2 text-sm text-gray-600"
          >
            <span class="inline-flex gap-1">
              <span class="h-2 w-2 animate-bounce rounded-full bg-gray-400" />
              <span class="h-2 w-2 animate-bounce rounded-full bg-gray-400" style="animation-delay: 0.15s" />
              <span class="h-2 w-2 animate-bounce rounded-full bg-gray-400" style="animation-delay: 0.3s" />
            </span>
            <button
              type="button"
              class="ml-3 text-xs text-blue-600 hover:underline"
              data-testid="abort-button"
            >
              Stop
            </button>
          </div>
        </div>

        <!-- assistant turn — completed response -->
        <div
          v-if="isResponded"
          class="flex justify-start"
          data-testid="assistant-message"
        >
          <div
            class="max-w-[80%] rounded-2xl rounded-tl-sm bg-gray-100 px-4 py-2 text-sm text-gray-800 whitespace-pre-line font-mono leading-relaxed"
          >{{ FIXTURE.assistantMessage }}</div>
        </div>

        <!-- usage strip on responded state -->
        <div
          v-if="isResponded"
          class="flex justify-end gap-3 text-xs text-gray-500"
          data-testid="usage-strip"
        >
          <span>input <strong>{{ FIXTURE.usageInputTokens }}</strong> tok</span>
          <span>output <strong>{{ FIXTURE.usageOutputTokens }}</strong> tok</span>
          <span>est. cost <strong>{{ FIXTURE.estimatedCost }}</strong></span>
        </div>

        <!-- error banner -->
        <div
          v-if="isError"
          class="rounded-md border border-red-200 bg-red-50 p-4"
          data-testid="error-banner"
        >
          <div class="flex items-start gap-3">
            <span class="text-xl text-red-500" aria-hidden="true">⚠︎</span>
            <div>
              <p class="font-semibold text-red-700">{{ FIXTURE.errorTitle }}</p>
              <p class="mt-1 text-sm text-red-600">{{ FIXTURE.errorBody }}</p>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Composer (visual only — disabled while typing / errored) -->
    <footer class="mt-4 border-t pt-4">
      <div class="flex gap-2">
        <input
          type="text"
          class="flex-1 rounded-md border border-gray-300 px-3 py-2 text-sm focus:border-blue-500 focus:outline-none disabled:bg-gray-50"
          :placeholder="FIXTURE.inputPlaceholder"
          :disabled="isTyping"
          data-testid="composer-input"
        />
        <button
          type="button"
          class="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50"
          :disabled="isTyping || isError"
          data-testid="send-button"
        >
          Send
        </button>
      </div>
      <p class="mt-2 text-xs text-gray-400" data-testid="system-prompt-hint">
        {{ FIXTURE.systemPromptHint }}
      </p>
    </footer>
  </section>
</template>

<style scoped>
@keyframes bounce {
  0%, 100% { transform: translateY(0); opacity: 0.4; }
  50% { transform: translateY(-3px); opacity: 1; }
}
.animate-bounce {
  animation: bounce 0.9s infinite ease-in-out;
}
</style>
