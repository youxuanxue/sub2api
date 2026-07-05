<template>
  <AppLayout>
    <div class="mx-auto max-w-3xl space-y-8 py-4">
      <!-- Header -->
      <div class="text-center">
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white sm:text-3xl">
          {{ t('quickstart.title') }}
        </h1>
        <p class="mt-2 text-gray-500 dark:text-gray-400">
          {{ t('quickstart.subtitle') }}
        </p>
      </div>

      <!-- Step 1: API Key -->
      <section class="rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800">
        <div class="mb-4 flex items-center gap-3">
          <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-teal-500 text-sm font-bold text-white">1</span>
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('quickstart.step1Title') }}</h2>
        </div>

        <div v-if="keyLoading" class="flex items-center justify-center py-6">
          <LoadingSpinner />
        </div>
        <div v-else-if="apiKey" class="space-y-3">
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium text-gray-500 dark:text-gray-400">API Key</span>
            <span class="rounded bg-teal-50 px-2 py-0.5 text-xs font-medium text-teal-700 dark:bg-teal-900/30 dark:text-teal-300">{{ apiKey.name }}</span>
          </div>
          <div class="flex items-center gap-2">
            <code class="flex-1 rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 font-mono text-sm text-gray-800 dark:border-gray-600 dark:bg-gray-900 dark:text-gray-200">
              {{ showKey ? apiKey.key : maskKey(apiKey.key) }}
            </code>
            <button @click="showKey = !showKey" class="btn-icon" :title="showKey ? 'Hide' : 'Show'">
              <Icon :name="showKey ? 'eyeOff' : 'eye'" size="sm" />
            </button>
            <button @click="copyToClipboard(apiKey.key, 'key')" class="btn-icon" :title="t('common.copy')">
              <Icon :name="copied === 'key' ? 'check' : 'copy'" size="sm" :class="copied === 'key' ? 'text-teal-500' : ''" />
            </button>
          </div>
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium text-gray-500 dark:text-gray-400">Base URL</span>
          </div>
          <div class="flex items-center gap-2">
            <code class="flex-1 rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 font-mono text-sm text-gray-800 dark:border-gray-600 dark:bg-gray-900 dark:text-gray-200">
              {{ baseUrl }}
            </code>
            <button @click="copyToClipboard(baseUrl, 'url')" class="btn-icon" :title="t('common.copy')">
              <Icon :name="copied === 'url' ? 'check' : 'copy'" size="sm" :class="copied === 'url' ? 'text-teal-500' : ''" />
            </button>
          </div>
        </div>
        <div v-else-if="keyError" class="text-sm text-red-500">{{ keyError }}</div>
      </section>

      <!-- Step 2: Tool config -->
      <section class="rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800">
        <div class="mb-4 flex items-center gap-3">
          <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-teal-500 text-sm font-bold text-white">2</span>
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('quickstart.step2Title') }}</h2>
        </div>

        <!-- Tool tabs -->
        <div class="mb-4 flex flex-wrap gap-2 border-b border-gray-200 pb-3 dark:border-gray-700">
          <button
            v-for="tool in tools"
            :key="tool.id"
            @click="activeTool = tool.id"
            :class="[
              'rounded-lg px-3 py-1.5 text-sm font-medium transition-colors',
              activeTool === tool.id
                ? 'bg-teal-500 text-white'
                : 'text-gray-600 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-gray-700'
            ]"
          >
            {{ tool.label }}
          </button>
        </div>

        <!-- Config snippet -->
        <div class="relative">
          <div class="rounded-lg border border-gray-200 bg-gray-900 dark:border-gray-600">
            <div class="flex items-center justify-between border-b border-gray-700 px-4 py-2">
              <span class="text-xs text-gray-400">{{ activeToolConfig.file }}</span>
              <button
                @click="copyToClipboard(activeToolConfig.snippet, 'snippet')"
                :class="[
                  'flex items-center gap-1 rounded px-2 py-1 text-xs transition-colors',
                  copied === 'snippet'
                    ? 'text-teal-400'
                    : 'text-gray-400 hover:text-white'
                ]"
              >
                <Icon :name="copied === 'snippet' ? 'check' : 'copy'" size="xs" />
                {{ copied === 'snippet' ? t('quickstart.copied') : t('quickstart.copyConfig') }}
              </button>
            </div>
            <pre class="overflow-x-auto p-4 font-mono text-sm leading-relaxed text-gray-100"><code>{{ activeToolConfig.snippet }}</code></pre>
          </div>
          <p v-if="activeToolConfig.note" class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ activeToolConfig.note }}
          </p>
        </div>
      </section>

      <!-- Step 3: Test -->
      <section class="rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800">
        <div class="mb-4 flex items-center gap-3">
          <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-teal-500 text-sm font-bold text-white">3</span>
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('quickstart.step3Title') }}</h2>
        </div>
        <div class="relative">
          <div class="rounded-lg border border-gray-200 bg-gray-900 dark:border-gray-600">
            <div class="flex items-center justify-between border-b border-gray-700 px-4 py-2">
              <span class="text-xs text-gray-400">Terminal</span>
              <button
                @click="copyToClipboard(curlSnippet, 'curl')"
                :class="[
                  'flex items-center gap-1 rounded px-2 py-1 text-xs transition-colors',
                  copied === 'curl' ? 'text-teal-400' : 'text-gray-400 hover:text-white'
                ]"
              >
                <Icon :name="copied === 'curl' ? 'check' : 'copy'" size="xs" />
                {{ copied === 'curl' ? t('quickstart.copied') : t('quickstart.copyConfig') }}
              </button>
            </div>
            <pre class="overflow-x-auto p-4 font-mono text-sm leading-relaxed text-gray-100"><code>{{ curlSnippet }}</code></pre>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('quickstart.step3Note') }}</p>
        </div>
      </section>

      <!-- Next steps -->
      <div class="flex flex-wrap items-center justify-center gap-4 pb-6">
        <router-link to="/keys" class="btn btn-secondary text-sm">{{ t('quickstart.manageKeys') }}</router-link>
        <router-link to="/pricing" class="btn btn-secondary text-sm">{{ t('quickstart.viewPricing') }}</router-link>
        <router-link to="/studio" class="btn btn-primary text-sm">{{ t('quickstart.tryStudio') }}</router-link>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import * as keysAPI from '@/api/keys'
import type { ApiKey } from '@/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()

const apiKey = ref<ApiKey | null>(null)
const keyLoading = ref(true)
const keyError = ref('')
const showKey = ref(true)
const copied = ref<string | null>(null)
const activeTool = ref('claude-code')

const baseUrl = computed(() => {
  const raw = appStore.cachedPublicSettings?.api_base_url || window.location.origin
  return raw.replace(/\/+$/, '')
})

const keyValue = computed(() => apiKey.value?.key || 'YOUR_API_KEY')

const tools = [
  { id: 'claude-code', label: 'Claude Code' },
  { id: 'cursor', label: 'Cursor' },
  { id: 'codex', label: 'Codex CLI' },
  { id: 'cline', label: 'Cline' },
  { id: 'python', label: 'Python' },
  { id: 'nodejs', label: 'Node.js' },
  { id: 'curl', label: 'cURL' },
]

function toolConfig(toolId: string) {
  const key = keyValue.value
  const url = baseUrl.value

  switch (toolId) {
    case 'claude-code':
      return {
        file: '~/.bashrc / ~/.zshrc',
        snippet: `export ANTHROPIC_BASE_URL=${url}
export ANTHROPIC_API_KEY=${key}`,
        note: t('quickstart.claudeCodeNote'),
      }
    case 'cursor':
      return {
        file: 'Cursor Settings → Models → OpenAI API Key',
        snippet: `API Key:  ${key}
Base URL: ${url}/v1`,
        note: t('quickstart.cursorNote'),
      }
    case 'codex':
      return {
        file: '~/.bashrc / ~/.zshrc',
        snippet: `export OPENAI_API_KEY=${key}
export OPENAI_BASE_URL=${url}/v1`,
        note: t('quickstart.codexNote'),
      }
    case 'cline':
      return {
        file: 'Cline Settings → API Provider → OpenAI Compatible',
        snippet: `API Key:  ${key}
Base URL: ${url}/v1`,
        note: t('quickstart.clineNote'),
      }
    case 'python':
      return {
        file: 'main.py',
        snippet: `from openai import OpenAI

client = OpenAI(
    api_key="${key}",
    base_url="${url}/v1"
)

response = client.chat.completions.create(
    model="claude-sonnet-4-20250514",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)`,
        note: t('quickstart.pythonNote'),
      }
    case 'nodejs':
      return {
        file: 'index.js',
        snippet: `import OpenAI from 'openai';

const client = new OpenAI({
  apiKey: '${key}',
  baseURL: '${url}/v1'
});

const response = await client.chat.completions.create({
  model: 'claude-sonnet-4-20250514',
  messages: [{ role: 'user', content: 'Hello!' }]
});
console.log(response.choices[0].message.content);`,
        note: t('quickstart.nodejsNote'),
      }
    case 'curl':
      return {
        file: 'Terminal',
        snippet: `curl ${url}/v1/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${key}" \\
  -d '{
    "model": "claude-sonnet-4-20250514",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`,
        note: '',
      }
    default:
      return { file: '', snippet: '', note: '' }
  }
}

const activeToolConfig = computed(() => toolConfig(activeTool.value))

const curlSnippet = computed(() => {
  const key = keyValue.value
  const url = baseUrl.value
  return `curl ${url}/v1/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${key}" \\
  -d '{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hello!"}]}'`
})

function maskKey(key: string) {
  if (key.length <= 8) return '••••••••'
  return key.slice(0, 4) + '•'.repeat(key.length - 8) + key.slice(-4)
}

async function copyToClipboard(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = label
    setTimeout(() => { copied.value = null }, 2000)
  } catch { /* clipboard access denied in some contexts */ }
}

async function initKey() {
  keyLoading.value = true
  keyError.value = ''
  try {
    const result = await keysAPI.list(1, 1)
    if (result.items && result.items.length > 0) {
      apiKey.value = result.items[0]
    } else {
      const newKey = await keysAPI.create('Quick Start', undefined, undefined, undefined, undefined, undefined, undefined, undefined, 'universal')
      apiKey.value = newKey
    }
  } catch (e: unknown) {
    keyError.value = e instanceof Error ? e.message : String(e)
  } finally {
    keyLoading.value = false
  }
}

onMounted(() => {
  initKey()
})
</script>

<style scoped>
.btn-icon {
  @apply rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-gray-700;
}
</style>
