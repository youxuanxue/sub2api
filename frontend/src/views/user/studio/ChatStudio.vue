<template>
  <div class="flex flex-col gap-5">
    <!-- Controls: the SHELL owns the key picker; chat owns model + sampling. -->
    <div class="flex flex-wrap items-end gap-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
      <div class="min-w-[220px] flex-1">
        <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400" for="chat-model">{{
          t('studio.chat.model')
        }}</label>
        <select
          id="chat-model"
          v-model="selectedModelId"
          class="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
          :disabled="!chatModels.length || sending"
        >
          <option v-if="!chatModels.length" disabled value="">{{ t('studio.chat.pickModelPlaceholder') }}</option>
          <option v-for="m in chatModels" :key="m" :value="m">{{ m }}</option>
        </select>
      </div>
      <div class="min-w-[80px]">
        <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400" for="chat-temp">{{
          t('studio.chat.temperature')
        }}</label>
        <input
          id="chat-temp"
          v-model.number="temperature"
          type="number"
          min="0"
          max="2"
          step="0.1"
          class="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm dark:border-dark-600 dark:bg-dark-950 dark:text-white"
          :disabled="sending"
        />
      </div>
      <div class="min-w-[100px]">
        <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400" for="chat-max">{{
          t('studio.chat.maxTokens')
        }}</label>
        <input
          id="chat-max"
          v-model.number="maxTokens"
          type="number"
          min="1"
          :max="PLAYGROUND_MAX_TOKENS_CAP"
          step="1"
          class="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm dark:border-dark-600 dark:bg-dark-950 dark:text-white"
          :disabled="sending"
        />
      </div>
    </div>

    <div
      v-if="!chatModels.length"
      class="rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/40 dark:text-amber-100"
    >
      {{ t('studio.chat.noModels') }}
    </div>

    <div class="grid gap-6 lg:grid-cols-[1fr_minmax(240px,280px)]">
      <div
        class="flex min-h-[420px] flex-col rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900"
      >
        <div class="scrollbar-thin flex-1 space-y-4 overflow-y-auto p-4">
          <div v-if="!displayMessages.length" class="py-16 text-center text-sm text-gray-500 dark:text-dark-400">
            {{ t('studio.chat.emptyHint') }}
          </div>
          <!-- WeChat-style: assistant left + user right, with side avatars -->
          <div
            v-for="(msg, idx) in displayMessages"
            :key="idx"
            :class="[
              'flex w-full gap-2.5',
              msg.role === 'user' ? 'flex-row-reverse justify-start' : 'flex-row justify-start'
            ]"
          >
            <div
              class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-xs font-semibold text-white shadow-sm"
              :class="
                msg.role === 'user' ? 'bg-primary-600 dark:bg-primary-500' : 'bg-dark-600 dark:bg-dark-500'
              "
              aria-hidden="true"
            >
              {{ msg.role === 'user' ? t('studio.chat.avatarUser') : t('studio.chat.avatarAssistant') }}
            </div>
            <div
              :class="[
                'flex min-w-0 max-w-[min(85%,28rem)] flex-col gap-0.5',
                msg.role === 'user' ? 'items-end text-right' : 'items-start text-left'
              ]"
            >
              <div class="text-[11px] font-medium uppercase tracking-wide text-gray-400 dark:text-dark-500">
                {{ roleLabel(msg.role) }}
              </div>
              <div
                class="max-w-none whitespace-pre-wrap break-words px-3.5 py-2.5 text-sm shadow-sm"
                :class="
                  msg.role === 'user'
                    ? 'rounded-2xl rounded-tr-md bg-primary-600 text-white dark:bg-primary-600'
                    : 'rounded-2xl rounded-tl-md bg-gray-50 text-gray-900 dark:bg-dark-800 dark:text-dark-100'
                "
              >
                <div
                  v-if="msg.role === 'assistant'"
                  class="prose prose-sm max-w-none dark:prose-invert prose-p:my-1 prose-pre:my-2 [&_a]:text-primary-600 dark:[&_a]:text-primary-300"
                  v-html="renderMarkdown(msg.content)"
                />
                <span v-else>{{ msg.content }}</span>
              </div>
            </div>
          </div>
        </div>
        <div class="border-t border-gray-100 p-4 dark:border-dark-700">
          <textarea
            v-model="draft"
            rows="3"
            class="mb-3 w-full resize-none rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-950 dark:text-white dark:placeholder:text-dark-500"
            :placeholder="t('studio.chat.inputPlaceholder')"
            :disabled="sending || !apiKey"
            @keydown.enter.exact.prevent="submit"
          />
          <div class="flex flex-wrap items-center gap-3">
            <button
              type="button"
              class="inline-flex items-center rounded-lg bg-primary-600 px-4 py-2 text-sm font-medium text-white hover:bg-primary-700 disabled:opacity-50"
              :disabled="sending || !apiKey || !draft.trim() || !selectedModelId"
              @click="submit"
            >
              {{ sending ? t('studio.chat.sending') : t('studio.chat.send') }}
            </button>
            <button
              v-if="sending && abortCtrl"
              type="button"
              class="text-sm font-medium text-gray-600 underline dark:text-dark-300"
              @click="abortCtrl.abort()"
            >
              {{ t('studio.chat.cancel') }}
            </button>
            <button
              type="button"
              class="text-sm text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-dark-200"
              @click="clearConversation"
            >
              {{ t('studio.chat.clear') }}
            </button>
          </div>
        </div>
      </div>

      <aside class="space-y-4 rounded-xl border border-gray-200 bg-gray-50/80 p-4 dark:border-dark-700 dark:bg-dark-800/40">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('studio.chat.systemPrompt') }}</h3>
          <textarea
            v-model="systemPromptLocal"
            rows="6"
            class="mt-2 w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-xs text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
            :disabled="sending"
          />
        </div>
        <p class="text-xs text-gray-500 dark:text-dark-400">
          {{ t('studio.chat.limitsHint', { turns: PLAYGROUND_MAX_TURNS, maxTok: PLAYGROUND_MAX_TOKENS_CAP }) }}
        </p>
        <div class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-900">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('studio.chat.integrationsTitle') }}</h3>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('studio.chat.integrationsHint') }}</p>
          <div class="mt-3 grid grid-cols-2 gap-2">
            <button
              v-for="client in TK_CLIENT_INTEGRATIONS"
              :key="client.id"
              type="button"
              class="inline-flex min-h-9 flex-col items-center justify-center rounded-lg border border-gray-200 bg-white px-2 py-1.5 text-xs font-medium text-gray-700 hover:border-primary-400 hover:text-primary-600 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-600 dark:bg-dark-950 dark:text-dark-200 dark:hover:border-primary-500 dark:hover:text-primary-400"
              :disabled="!apiKey"
              :title="client.carriesApiKey
                ? t('studio.chat.integrationsAppHint')
                : t('studio.chat.integrationsManualKeyHint')"
              @click="openIntegration(client)"
            >
              {{ client.name }}
              <span v-if="!client.carriesApiKey" class="text-[10px] font-normal text-amber-600 dark:text-amber-300">
                {{ t('studio.chat.integrationsManualKeyShort') }}
              </span>
            </button>
          </div>
          <div class="mt-3 flex flex-wrap gap-3 border-t border-gray-100 pt-2 text-xs dark:border-dark-700">
            <button
              type="button"
              class="font-medium text-primary-600 hover:underline dark:text-primary-400"
              @click="copyToClipboard(gatewayBase, t('studio.chat.baseUrlCopied'))"
            >
              {{ t('studio.chat.copyBaseUrl') }}
            </button>
            <button
              type="button"
              class="font-medium text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400"
              :disabled="!apiKey"
              @click="copyToClipboard(apiKey, t('studio.chat.keyCopied'))"
            >
              {{ t('studio.chat.copyKey') }}
            </button>
          </div>
        </div>
        <div
          v-if="lastUsage"
          class="rounded-lg border border-gray-200 bg-white p-3 text-xs dark:border-dark-600 dark:bg-dark-900"
        >
          <div class="font-medium text-gray-800 dark:text-dark-100">{{ t('studio.chat.lastUsage') }}</div>
          <dl class="mt-2 space-y-1 text-gray-600 dark:text-dark-300">
            <div class="flex justify-between gap-2">
              <dt>{{ t('studio.chat.promptTokens') }}</dt>
              <dd>{{ lastUsage.prompt_tokens ?? '—' }}</dd>
            </div>
            <div class="flex justify-between gap-2">
              <dt>{{ t('studio.chat.completionTokens') }}</dt>
              <dd>{{ lastUsage.completion_tokens ?? '—' }}</dd>
            </div>
            <div class="flex justify-between gap-2">
              <dt>{{ t('studio.chat.totalTokens') }}</dt>
              <dd>{{ lastUsage.total_tokens ?? '—' }}</dd>
            </div>
          </dl>
        </div>
        <div
          v-if="requestError"
          class="rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-800 dark:border-red-900/50 dark:bg-red-950/40 dark:text-red-100"
          data-testid="studio-chat-error"
        >
          {{ requestError }}
        </div>
      </aside>
    </div>
  </div>
</template>

<script setup lang="ts">
// TK: the chat surface, folded in from the retired /playground as the Studio's
// first-class fourth modality. The SHELL (MediaStudioView) owns key selection and
// the /v1/models probe; this component derives its chat model list from the
// passed-in pool and calls /v1/chat/completions directly. No cost-gate / balance
// check (chat is token-metered after the fact, same as the old playground).
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import {
  gatewayChatCompletion,
  PLAYGROUND_DEFAULT_MAX_TOKENS,
  PLAYGROUND_MAX_TOKENS_CAP,
  PLAYGROUND_MAX_TURNS,
  type ChatMessage
} from '@/api/playground'
import { modalityForModel } from '@/constants/playgroundMedia.tk'
import {
  resolveTkClientIntegrationUrl,
  TK_CLIENT_INTEGRATIONS,
  type TkClientIntegration
} from '@/constants/clientIntegrations.tk'
import { useClipboard } from '@/composables/useClipboard'

const props = defineProps<{
  apiKey: string
  gatewayBase: string
  availableIds: Set<string>
}>()

const { t } = useI18n()
const { copyToClipboard } = useClipboard()

marked.setOptions({ gfm: true, breaks: true })

// Chat models = the selected key's group pool, filtered to chat-classified ids
// (the shell already probed /v1/models, so no extra fetch). Sorted for stability.
const chatModels = computed(() =>
  [...props.availableIds].filter((id) => modalityForModel(id) === 'chat').sort()
)
const selectedModelId = ref('')

const temperature = ref(1)
const maxTokens = ref(PLAYGROUND_DEFAULT_MAX_TOKENS)
const systemPromptLocal = ref('')
const draft = ref('')
const chatMessages = ref<ChatMessage[]>([])
const sending = ref(false)
const requestError = ref('')
const abortCtrl = ref<AbortController | null>(null)
const lastUsage = ref<{ prompt_tokens?: number; completion_tokens?: number; total_tokens?: number } | null>(null)

const displayMessages = computed(() => chatMessages.value.filter((m) => m.role !== 'system'))

// Keep a valid model selected as the key (→ pool) changes under us.
watch(
  chatModels,
  (list) => {
    if (!list.includes(selectedModelId.value)) {
      selectedModelId.value = list[0] ?? ''
    }
  },
  { immediate: true }
)

function renderMarkdown(content: string): string {
  return DOMPurify.sanitize(marked.parse(content || '') as string)
}

function roleLabel(role: string): string {
  if (role === 'user') return t('studio.chat.roleUser')
  if (role === 'assistant') return t('studio.chat.roleAssistant')
  return role
}

/** Keep at most PLAYGROUND_MAX_TURNS user/assistant pairs (browser-only context). */
function trimConversation(msgs: ChatMessage[]): ChatMessage[] {
  const rest = msgs.filter((m) => m.role === 'user' || m.role === 'assistant')
  while (rest.length > PLAYGROUND_MAX_TURNS * 2) {
    rest.splice(0, 2)
  }
  return rest
}

function openIntegration(client: TkClientIntegration): void {
  if (!props.apiKey) return
  const url = resolveTkClientIntegrationUrl({
    template: client.template,
    apiKey: props.apiKey,
    baseUrl: props.gatewayBase,
    model: selectedModelId.value
  })
  const target = window.open(url, '_blank', 'noopener,noreferrer')
  if (target) target.opener = null
}

function clearConversation(): void {
  chatMessages.value = []
  lastUsage.value = null
  requestError.value = ''
}

async function submit(): Promise<void> {
  const text = draft.value.trim()
  if (!text || !props.apiKey || !selectedModelId.value || sending.value) return

  requestError.value = ''
  const sys = systemPromptLocal.value.trim()
  const userMsg: ChatMessage = { role: 'user', content: text }
  chatMessages.value = trimConversation([...chatMessages.value, userMsg])
  draft.value = ''

  const payloadMessages: ChatMessage[] = []
  if (sys) payloadMessages.push({ role: 'system', content: sys })
  payloadMessages.push(...chatMessages.value.filter((m) => m.role !== 'system'))

  const capped = Math.min(Math.max(1, maxTokens.value || PLAYGROUND_DEFAULT_MAX_TOKENS), PLAYGROUND_MAX_TOKENS_CAP)

  sending.value = true
  abortCtrl.value = new AbortController()
  try {
    const raw = (await gatewayChatCompletion(
      props.apiKey,
      props.gatewayBase,
      {
        model: selectedModelId.value,
        messages: payloadMessages,
        temperature: temperature.value,
        max_tokens: capped
      },
      abortCtrl.value.signal
    )) as Record<string, unknown>

    const choice = raw?.choices && Array.isArray(raw.choices) ? (raw.choices[0] as Record<string, unknown>) : null
    const msg = choice?.message as Record<string, unknown> | undefined
    const content = typeof msg?.content === 'string' ? msg.content : ''
    chatMessages.value = trimConversation([
      ...chatMessages.value,
      { role: 'assistant', content: content || '(empty)' }
    ])

    const usage = raw?.usage as Record<string, unknown> | undefined
    if (usage) {
      lastUsage.value = {
        prompt_tokens: typeof usage.prompt_tokens === 'number' ? usage.prompt_tokens : undefined,
        completion_tokens: typeof usage.completion_tokens === 'number' ? usage.completion_tokens : undefined,
        total_tokens: typeof usage.total_tokens === 'number' ? usage.total_tokens : undefined
      }
    }
  } catch (e) {
    const err = e as Error
    requestError.value = err.name === 'AbortError' ? t('studio.chat.cancelled') : err.message || t('studio.chat.requestFailed')
  } finally {
    sending.value = false
    abortCtrl.value = null
  }
}
</script>
