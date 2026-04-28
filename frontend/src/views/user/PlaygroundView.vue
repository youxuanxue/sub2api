<template>
  <AppLayout>
    <div class="mx-auto flex max-w-6xl flex-col gap-6 px-4 py-6 lg:px-6">
      <header class="space-y-2">
        <h1 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white">{{ t('playground.title') }}</h1>
        <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('playground.subtitle') }}</p>
      </header>

      <div
        v-if="loadError"
        class="rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/40 dark:text-amber-100"
      >
        {{ loadError }}
        <router-link class="ml-2 font-medium text-primary-600 underline dark:text-primary-400" to="/keys">
          {{ t('playground.manageKeys') }}
        </router-link>
      </div>

      <div class="flex flex-wrap items-end gap-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <div class="min-w-[200px] flex-1">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400" for="pg-model">{{
            t('playground.model')
          }}</label>
              <select
            id="pg-model"
            v-model="selectedModelId"
            class="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
            :disabled="!models.length || sending || !!loadError"
          >
            <option v-if="!models.length" disabled value="">{{ t('playground.pickModelPlaceholder') }}</option>
            <option v-for="m in models" :key="m.id" :value="m.id">{{ m.id }}</option>
          </select>
        </div>
        <div class="min-w-[80px]">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400" for="pg-temp">{{
            t('playground.temperature')
          }}</label>
          <input
            id="pg-temp"
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
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400" for="pg-max">{{
            t('playground.maxTokens')
          }}</label>
          <input
            id="pg-max"
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

      <div class="grid gap-6 lg:grid-cols-[1fr_minmax(240px,280px)]">
        <div class="flex min-h-[420px] flex-col rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900">
          <div class="scrollbar-thin flex-1 space-y-4 overflow-y-auto p-4">
            <div v-if="!displayMessages.length" class="py-16 text-center text-sm text-gray-500 dark:text-dark-400">
              {{ t('playground.emptyHint') }}
            </div>
            <!-- WeChat-style: assistant left + user right, with side avatars -->
            <div
              v-for="(msg, idx) in displayMessages"
              :key="idx"
              :class="[
                'flex w-full gap-2.5',
                /* row-reverse: main-start is right; justify-start packs user turn to the right (chat convention) */
                msg.role === 'user' ? 'flex-row-reverse justify-start' : 'flex-row justify-start'
              ]"
            >
              <div
                class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-xs font-semibold text-white shadow-sm"
                :class="
                  msg.role === 'user'
                    ? 'bg-primary-600 dark:bg-primary-500'
                    : 'bg-dark-600 dark:bg-dark-500'
                "
                aria-hidden="true"
              >
                {{ msg.role === 'user' ? t('playground.avatarUser') : t('playground.avatarAssistant') }}
              </div>
              <div
                :class="[
                  'flex min-w-0 max-w-[min(85%,28rem)] flex-col gap-0.5',
                  msg.role === 'user' ? 'items-end text-right' : 'items-start text-left'
                ]"
              >
                <div
                  class="text-[11px] font-medium uppercase tracking-wide text-gray-400 dark:text-dark-500"
                >
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
                  <span v-else>{{ escapeHtml(msg.content) }}</span>
                </div>
              </div>
            </div>
          </div>
          <div class="border-t border-gray-100 p-4 dark:border-dark-700">
            <textarea
              v-model="draft"
              rows="3"
              class="mb-3 w-full resize-none rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-950 dark:text-white dark:placeholder:text-dark-500"
              :placeholder="t('playground.inputPlaceholder')"
              :disabled="sending || !apiKey"
              @keydown.enter.exact.prevent="submit"
            />
            <div class="flex flex-wrap items-center gap-3">
              <button
                type="button"
                class="inline-flex items-center rounded-lg bg-primary-600 px-4 py-2 text-sm font-medium text-white hover:bg-primary-700 disabled:opacity-50"
                :disabled="sending || !apiKey || !draft.trim()"
                @click="submit"
              >
                {{ sending ? t('playground.sending') : t('playground.send') }}
              </button>
              <button
                v-if="sending && abortCtrl"
                type="button"
                class="text-sm font-medium text-gray-600 underline dark:text-dark-300"
                @click="abortCtrl.abort()"
              >
                {{ t('playground.cancel') }}
              </button>
              <button
                type="button"
                class="text-sm text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-dark-200"
                @click="clearConversation"
              >
                {{ t('playground.clear') }}
              </button>
            </div>
          </div>
        </div>

        <aside class="space-y-4 rounded-xl border border-gray-200 bg-gray-50/80 p-4 dark:border-dark-700 dark:bg-dark-800/40">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('playground.systemPrompt') }}</h3>
            <textarea
              v-model="systemPromptLocal"
              rows="6"
              class="mt-2 w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-xs text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
              :disabled="sending"
            />
          </div>
          <p class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('playground.limitsHint', { turns: PLAYGROUND_MAX_TURNS, maxTok: PLAYGROUND_MAX_TOKENS_CAP }) }}
          </p>
          <div v-if="lastUsage" class="rounded-lg border border-gray-200 bg-white p-3 text-xs dark:border-dark-600 dark:bg-dark-900">
            <div class="font-medium text-gray-800 dark:text-dark-100">{{ t('playground.lastUsage') }}</div>
            <dl class="mt-2 space-y-1 text-gray-600 dark:text-dark-300">
              <div class="flex justify-between gap-2">
                <dt>{{ t('playground.promptTokens') }}</dt>
                <dd>{{ lastUsage.prompt_tokens ?? '—' }}</dd>
              </div>
              <div class="flex justify-between gap-2">
                <dt>{{ t('playground.completionTokens') }}</dt>
                <dd>{{ lastUsage.completion_tokens ?? '—' }}</dd>
              </div>
              <div class="flex justify-between gap-2">
                <dt>{{ t('playground.totalTokens') }}</dt>
                <dd>{{ lastUsage.total_tokens ?? '—' }}</dd>
              </div>
            </dl>
          </div>
          <div
            v-if="requestError"
            class="rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-800 dark:border-red-900/50 dark:bg-red-950/40 dark:text-red-100"
            data-testid="playground-error-banner"
          >
            {{ requestError }}
          </div>
        </aside>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import AppLayout from '@/components/layout/AppLayout.vue'
import { keysAPI } from '@/api/keys'
import {
  gatewayChatCompletion,
  gatewayListModels,
  PLAYGROUND_DEFAULT_MAX_TOKENS,
  PLAYGROUND_MAX_TOKENS_CAP,
  PLAYGROUND_MAX_TURNS,
  resolveGatewayBaseUrl,
  type ChatMessage,
  type GatewayModelEntry
} from '@/api/playground'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()

marked.setOptions({ gfm: true, breaks: true })

const apiKey = ref<string>('')
const gatewayBase = ref('')
const models = ref<GatewayModelEntry[]>([])
const selectedModelId = ref('')
const temperature = ref(1)
const maxTokens = ref(PLAYGROUND_DEFAULT_MAX_TOKENS)
const systemPromptLocal = ref('')
const draft = ref('')
const chatMessages = ref<ChatMessage[]>([])
const sending = ref(false)
const loadError = ref('')
const requestError = ref('')
const abortCtrl = ref<AbortController | null>(null)

const lastUsage = ref<{ prompt_tokens?: number; completion_tokens?: number; total_tokens?: number } | null>(null)

const displayMessages = computed(() => chatMessages.value.filter((m) => m.role !== 'system'))

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

function renderMarkdown(content: string): string {
  const html = marked.parse(content || '') as string
  return DOMPurify.sanitize(html)
}

function roleLabel(role: string): string {
  if (role === 'user') return t('playground.roleUser')
  if (role === 'assistant') return t('playground.roleAssistant')
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

async function bootstrap(): Promise<void> {
  loadError.value = ''
  await appStore.fetchPublicSettings()
  gatewayBase.value = resolveGatewayBaseUrl(appStore.apiBaseUrl || appStore.cachedPublicSettings?.api_base_url)

  try {
    const page = await keysAPI.list(1, 50, { status: 'active' })
    const rows = page.items || []
    const trial = rows.find((k) => k.name?.toLowerCase() === 'trial')
    const pick = trial || rows[0]
    if (!pick?.key) {
      loadError.value = t('playground.noApiKey')
      return
    }
    apiKey.value = pick.key

    const list = await gatewayListModels(apiKey.value, gatewayBase.value)
    models.value = list.data || []
    if (models.value.length) {
      selectedModelId.value = models.value[0].id
    } else {
      loadError.value = t('playground.noModels')
    }
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : t('playground.loadFailed')
  }
}

function clearConversation(): void {
  chatMessages.value = []
  lastUsage.value = null
  requestError.value = ''
}

async function submit(): Promise<void> {
  const text = draft.value.trim()
  if (!text || !apiKey.value || !selectedModelId.value || sending.value) return

  requestError.value = ''
  const sys = systemPromptLocal.value.trim()
  const userMsg: ChatMessage = { role: 'user', content: text }
  const next = [...chatMessages.value, userMsg]
  chatMessages.value = trimConversation(next)
  draft.value = ''

  const payloadMessages: ChatMessage[] = []
  if (sys) {
    payloadMessages.push({ role: 'system', content: sys })
  }
  payloadMessages.push(...chatMessages.value.filter((m) => m.role !== 'system'))

  const capped = Math.min(Math.max(1, maxTokens.value || PLAYGROUND_DEFAULT_MAX_TOKENS), PLAYGROUND_MAX_TOKENS_CAP)

  sending.value = true
  abortCtrl.value = new AbortController()
  try {
    const raw = await gatewayChatCompletion(
      apiKey.value,
      gatewayBase.value,
      {
        model: selectedModelId.value,
        messages: payloadMessages,
        temperature: temperature.value,
        max_tokens: capped
      },
      abortCtrl.value.signal
    ) as Record<string, unknown>

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
    if (err.name === 'AbortError') {
      requestError.value = t('playground.cancelled')
    } else {
      requestError.value = err.message || t('playground.requestFailed')
    }
  } finally {
    sending.value = false
    abortCtrl.value = null
  }
}

onMounted(() => {
  void bootstrap()
})
</script>
