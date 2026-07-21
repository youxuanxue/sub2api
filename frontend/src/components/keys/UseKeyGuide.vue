<template>
  <div class="space-y-4">
      <!-- No Group Assigned Warning (direct keys only; universal keys skip this) -->
      <div v-if="!hasGuideContext" class="flex items-start gap-3 p-4 rounded-lg bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800">
        <svg class="w-5 h-5 text-yellow-500 flex-shrink-0 mt-0.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5">
          <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.007v.008H12v-.008z" />
        </svg>
        <div>
          <p class="text-sm font-medium text-yellow-800 dark:text-yellow-200">
            {{ t('keys.useKeyModal.noGroupTitle') }}
          </p>
          <p class="text-sm text-yellow-700 dark:text-yellow-300 mt-1">
            {{ t('keys.useKeyModal.noGroupDescription') }}
          </p>
        </div>
      </div>

      <!-- Platform-specific content -->
      <template v-else>
        <!-- Description -->
        <p v-if="!selectedClientEntry" class="text-sm text-gray-600 dark:text-gray-400">
          {{ platformDescription }}
        </p>

        <!-- Key essentials: model picker + locked base URL + masked key + live test.
             These are the error-prone fields; here they are picked/locked/verified
             rather than hand-typed (data-driven redesign — see useTkUseKey.ts). -->
        <div class="space-y-3 rounded-xl border border-gray-200 dark:border-dark-700 p-4 bg-gray-50/60 dark:bg-dark-800/40">
          <!-- CC-only group warning -->
          <div v-if="tkIsCCOnly" class="flex items-start gap-2 p-2.5 rounded-lg bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800">
            <Icon name="exclamationCircle" size="sm" class="text-red-500 flex-shrink-0 mt-0.5" />
            <p class="text-sm text-red-700 dark:text-red-300">{{ t('keys.useKeyModal.ccOnlyWarning') }}</p>
          </div>

          <!-- Model picker (single-model tabs only) -->
          <div v-if="activeFlavor" class="flex items-center gap-3 flex-wrap">
            <label class="w-14 text-sm font-medium text-gray-700 dark:text-gray-300 shrink-0">{{ t('keys.useKeyModal.modelLabel') }}</label>
            <select
              data-tk="use-key-model-select"
              :value="selectedModel"
              @change="onPickModel"
              :disabled="tkModelsLoading || !pickerModels.length"
              class="flex-1 min-w-[14rem] rounded-lg border border-gray-300 dark:border-dark-600 bg-white dark:bg-dark-900 px-3 py-1.5 text-sm font-mono text-gray-900 dark:text-gray-100 disabled:opacity-60"
            >
              <option v-if="!pickerModels.length" :value="selectedModel">{{ selectedModel }}</option>
              <option v-for="m in pickerModels" :key="m.id" :value="m.id">{{ m.id }}</option>
            </select>
            <div v-if="currentModelMeta" class="flex items-center gap-1.5 flex-wrap text-xs text-gray-500 dark:text-gray-400">
              <span v-if="currentModelMeta.contextWindow">{{ formatCtx(currentModelMeta.contextWindow) }}</span>
              <span
                v-for="c in currentModelMeta.capabilities"
                :key="c"
                class="px-1.5 py-0.5 rounded bg-gray-200 dark:bg-dark-700 text-gray-600 dark:text-gray-300"
              >{{ capabilityLabel(c) }}</span>
            </div>
          </div>
          <p v-if="activeFlavor && tkModelsLoading" class="text-xs text-gray-400 pl-[4.25rem]">{{ t('keys.useKeyModal.modelsLoading') }}</p>
          <p
            v-else-if="activeFlavor && showModelsCatalogEmpty"
            data-tk="use-key-models-empty"
            class="text-xs text-amber-600 dark:text-amber-400 pl-[4.25rem]"
          >{{ t('keys.useKeyModal.modelsEmpty') }}</p>

          <!-- Base URL (locked, read-only) -->
          <div class="flex items-center gap-3">
            <label class="w-14 text-sm font-medium text-gray-700 dark:text-gray-300 shrink-0">{{ t('keys.useKeyModal.baseUrlLabel') }}</label>
            <code class="flex-1 truncate rounded-lg border border-gray-200 dark:border-dark-700 bg-white dark:bg-dark-900 px-3 py-1.5 text-sm font-mono text-gray-700 dark:text-gray-200">{{ baseRoot }}</code>
            <button
              @click="copyText(baseRoot)"
              class="p-1.5 rounded-lg text-gray-400 hover:text-gray-700 hover:bg-gray-200 dark:hover:bg-dark-700 transition-colors"
              :title="t('keys.useKeyModal.copy')"
            >
              <Icon name="clipboard" size="sm" />
            </button>
          </div>

          <!-- API key (masked + reveal + copy) -->
          <div class="flex items-center gap-3">
            <label class="w-14 text-sm font-medium text-gray-700 dark:text-gray-300 shrink-0">{{ t('keys.useKeyModal.keyLabel') }}</label>
            <code class="flex-1 truncate rounded-lg border border-gray-200 dark:border-dark-700 bg-white dark:bg-dark-900 px-3 py-1.5 text-sm font-mono text-gray-700 dark:text-gray-200">{{ keyRevealed ? apiKey : maskedKey }}</code>
            <button
              @click="keyRevealed = !keyRevealed"
              class="p-1.5 rounded-lg text-gray-400 hover:text-gray-700 hover:bg-gray-200 dark:hover:bg-dark-700 transition-colors"
              :title="keyRevealed ? t('keys.useKeyModal.hide') : t('keys.useKeyModal.reveal')"
            >
              <Icon :name="keyRevealed ? 'eyeOff' : 'eye'" size="sm" />
            </button>
            <button
              @click="copyText(apiKey)"
              class="p-1.5 rounded-lg text-gray-400 hover:text-gray-700 hover:bg-gray-200 dark:hover:bg-dark-700 transition-colors"
              :title="t('keys.useKeyModal.copy')"
            >
              <Icon name="clipboard" size="sm" />
            </button>
          </div>

          <!-- Live test (single-model tabs only) -->
          <div v-if="activeFlavor" class="flex items-center gap-3 flex-wrap pt-1">
            <button
              @click="onTest"
              :disabled="tkTestState.status === 'running'"
              class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary-600 text-white hover:bg-primary-700 disabled:opacity-60 transition-colors"
            >
              <Icon v-if="tkTestState.status === 'running'" name="refresh" size="sm" class="animate-spin" />
              <span>
                {{ tkTestState.status === 'running'
                  ? t('keys.useKeyModal.testing')
                  : isDifyClient
                    ? t('quickstart.testToolCall')
                    : t('keys.useKeyModal.testKey') }}
              </span>
            </button>
            <span v-if="tkTestState.status === 'ok'" class="inline-flex items-center gap-1.5 text-sm text-green-600 dark:text-green-400">
              <Icon name="checkCircle" size="sm" />
              {{ tkTestState.httpStatus }} · {{ tkTestState.latencyMs }}ms · {{ tkTestState.toolCall
                ? t('quickstart.toolCallOk')
                : tkTestState.keyOnly
                  ? t('keys.useKeyModal.testKeyValid')
                  : t('keys.useKeyModal.testModelOk') }}
            </span>
            <span v-else-if="tkTestState.status === 'error'" class="inline-flex items-start gap-1.5 text-sm text-red-600 dark:text-red-400">
              <Icon name="exclamationCircle" size="sm" class="flex-shrink-0 mt-0.5" />
              <span class="break-all"><template v-if="tkTestState.httpStatus">{{ tkTestState.httpStatus }} · </template>{{ testErrorMessage }}</span>
            </span>
          </div>
        </div>

        <!-- Client Tabs -->
        <div v-if="showClientTabs && clientTabs.length" class="border-b border-gray-200 dark:border-dark-700">
          <nav class="-mb-px flex space-x-6" aria-label="Client">
            <button
              v-for="tab in clientTabs"
              :key="tab.id"
              @click="activeClientTab = tab.id"
              :class="[
                'whitespace-nowrap py-2.5 px-1 border-b-2 font-medium text-sm transition-colors',
                activeClientTab === tab.id
                  ? 'border-primary-500 text-primary-600 dark:text-primary-400'
                  : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300 dark:text-gray-400 dark:hover:text-gray-300'
              ]"
            >
              <span class="flex items-center gap-2">
                <component :is="tab.icon" class="w-4 h-4" />
                {{ tab.label }}
              </span>
            </button>
          </nav>
        </div>

        <!-- OS/Shell Tabs -->
        <div v-if="showShellTabs" data-tk="quickstart-environment-picker" class="border-b border-gray-200 dark:border-dark-700">
          <nav class="-mb-px flex flex-wrap gap-x-4" :aria-label="t('quickstart.environment')">
            <button
              v-for="tab in currentTabs"
              :key="tab.id"
              :data-tk="`quickstart-environment-${tab.id}`"
              :aria-pressed="activeTab === tab.id"
              @click="activeTab = tab.id"
              :class="[
                'whitespace-nowrap py-2.5 px-1 border-b-2 font-medium text-sm transition-colors',
                activeTab === tab.id
                  ? 'border-primary-500 text-primary-600 dark:text-primary-400'
                  : 'border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300 dark:text-gray-400 dark:hover:text-gray-300'
              ]"
            >
              <span class="flex items-center gap-2">
                <component :is="tab.icon" class="w-4 h-4" />
                {{ tab.label }}
              </span>
            </button>
          </nav>
        </div>

        <!-- Code Blocks (Stacked for multi-file platforms) -->
        <div class="space-y-4">
          <div
            v-for="(file, index) in currentFiles"
            :key="index"
            class="relative"
          >
            <!-- File Hint (if exists) -->
            <p v-if="file.hint" class="text-xs text-amber-600 dark:text-amber-400 mb-1.5 flex items-center gap-1">
              <Icon name="exclamationCircle" size="sm" class="flex-shrink-0" />
              {{ file.hint }}
            </p>
            <div class="bg-gray-900 dark:bg-dark-900 rounded-xl overflow-hidden">
              <!-- Code Header -->
              <div class="flex items-center justify-between px-4 py-2 bg-gray-800 dark:bg-dark-800 border-b border-gray-700 dark:border-dark-700">
                <span class="text-xs text-gray-400 font-mono">{{ file.path }}</span>
                <div class="flex items-center gap-2">
                  <button
                    v-if="isCollapsible(file)"
                    type="button"
                    :data-tk="`quickstart-config-toggle-${index}`"
                    class="flex items-center gap-1 px-2 py-1 text-xs font-medium text-gray-300 transition-colors hover:text-white"
                    @click="toggleExpandedFile(index)"
                  >
                    <Icon :name="expandedFileIndex === index ? 'chevronUp' : 'chevronDown'" size="xs" />
                    {{ expandedFileIndex === index ? t('quickstart.collapseConfig') : t('quickstart.expandConfig') }}
                  </button>
                  <button
                    @click="copyContent(file.content, index)"
                    class="flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded-lg transition-colors"
                    :class="copiedIndex === index
                      ? 'bg-green-500/20 text-green-400'
                      : 'bg-gray-700 hover:bg-gray-600 text-gray-300 hover:text-white'"
                  >
                    <svg v-if="copiedIndex === index" class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
                      <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
                    </svg>
                    <svg v-else class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5">
                      <path stroke-linecap="round" stroke-linejoin="round" d="M15.666 3.888A2.25 2.25 0 0013.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 01-.75.75H9a.75.75 0 01-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 01-2.25 2.25H6.75A2.25 2.25 0 014.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 011.927-.184" />
                    </svg>
                    {{ copiedIndex === index ? t('keys.useKeyModal.copied') : t('keys.useKeyModal.copy') }}
                  </button>
                </div>
              </div>
              <!-- Code Content -->
              <pre
                :data-tk="`quickstart-config-preview-${index}`"
                :class="{ 'max-h-80': isCollapsible(file) && expandedFileIndex !== index }"
                class="overflow-auto p-4 text-sm font-mono text-gray-100"
              ><code v-if="file.highlighted" v-html="file.highlighted"></code><code v-else v-text="file.content"></code></pre>
            </div>
          </div>
        </div>

        <!-- Usage Note -->
        <div v-if="showPlatformNote" class="flex items-start gap-3 p-3 rounded-lg bg-blue-50 dark:bg-blue-900/20 border border-blue-100 dark:border-blue-800">
          <Icon name="infoCircle" size="md" class="text-blue-500 flex-shrink-0 mt-0.5" />
          <p class="text-sm text-blue-700 dark:text-blue-300">
            {{ platformNote }}
          </p>
        </div>
      </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, h, watch, toRef, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'
import {
  useTkUseKey,
  capabilityLabel,
  anthropicEnvModel,
  claudeCodeEnvModel,
  flavorOfModel,
  type UseKeyFlavor,
  type UseKeyServableModel,
} from '@/composables/useTkUseKey'
import type { GroupPlatform, KeyRoutingMode } from '@/types'
import { TK_QUICKSTART_CLIENTS } from '@/constants/clientIntegrations.tk'
import { PLATFORM_ANTHROPIC, PLATFORM_ANTIGRAVITY, PLATFORM_GEMINI, PLATFORM_GROK, PLATFORM_NEWAPI, PLATFORM_OPENAI } from '@/constants/gatewayPlatforms'

interface Props {
  apiKey: string
  baseUrl: string
  platform: GroupPlatform | null
  /** universal keys have no fixed group/platform; guide is driven by model + client tab. */
  routingMode?: KeyRoutingMode
  /** The api key's numeric id — used to load its live servable model menu. */
  apiKeyId?: number | null
  /** Deep-link model id (e.g. from /pricing authorized-groups quick start). */
  initialModel?: string | null
  /** anthropic group gated to claude-cli / /v1/messages only (group.claude_code_only). */
  claudeCodeOnly?: boolean
  allowMessagesDispatch?: boolean
  // 分组的「支持的模型系列」(claude / gemini_text / gemini_image)。仅 antigravity 用：
  // 不含 'claude' 时隐藏 Claude flavor（Claude Code tab + OpenCode antigravity-claude
  // provider）。本指南只按 claude flavor 做粗粒度 gate；gemini_text 与 gemini_image 的
  // 细分仅后端 /antigravity/v1/models 生效。
  // 空/未传 = 不限制。
  supportedModelScopes?: string[]
  /** Current key limits, shown as Dify deployment ceilings. Zero means unlimited. */
  keyQuota?: number
  rateLimit5h?: number
  rateLimit1d?: number
  rateLimit7d?: number
  /** Controlled client surface used by /quickstart's tool-first picker. */
  selectedClient?: string | null
  /** Qwen Code supports Anthropic Messages and OpenAI Chat Completions. */
  selectedProtocol?: 'anthropic' | 'openai' | null
  /** Codex exposes WebSocket as a transport choice, not a separate client. */
  selectedTransport?: 'http' | 'websocket' | null
  /** Key modals keep legacy tabs; /quickstart owns client selection outside. */
  showClientTabs?: boolean
}

interface TabConfig {
  id: string
  label: string
  icon: Component
}

interface FileConfig {
  path: string
  content: string
  hint?: string  // Optional hint message for this file
  highlighted?: string
}

const props = withDefaults(defineProps<Props>(), {
  showClientTabs: true,
})
const emit = defineEmits<{
  modelChange: [model: string]
}>()
const showClientTabs = computed(() => props.showClientTabs)

const hasGuideContext = computed(
  () => props.routingMode === 'universal' || props.platform != null,
)

const { t } = useI18n()
const { copyToClipboard: clipboardCopy } = useClipboard()

const copiedIndex = ref<number | null>(null)
const activeTab = ref<string>('unix')
const activeClientTab = ref<string>('claude')
const expandedFileIndex = ref<number | null>(null)
const keyRevealed = ref(false)
const selectedClientEntry = computed(() =>
  props.selectedClient
    ? TK_QUICKSTART_CLIENTS.find((client) => client.guideId === props.selectedClient) ?? null
    : null,
)

// Gateway root with any trailing /v1 stripped — single source for the locked
// base-URL display and the live test request.
const baseRoot = computed(() =>
  (props.baseUrl || (typeof window !== 'undefined' ? window.location.origin : ''))
    .replace(/\/v1\/?$/, '')
    .replace(/\/+$/, ''),
)

// The single-model "flavor" the current client tab speaks. opencode is a
// multi-model catalog (no single pick), so it has no flavor.
const isOpenAIMessagesDispatchClaudeTab = computed(() =>
  activeClientTab.value === 'claude'
  && props.allowMessagesDispatch === true
  && (props.platform === PLATFORM_OPENAI || props.platform === PLATFORM_NEWAPI || props.platform === PLATFORM_GROK),
)

const activeFlavor = computed<UseKeyFlavor | null>(() => {
  const tab = activeClientTab.value
  if (selectedClientEntry.value?.guideMode === 'qwen') {
    return props.selectedProtocol === 'openai' ? 'openai' : 'anthropic'
  }
  if (selectedClientEntry.value?.guideMode === 'openai-fields') return 'openai'
  if (tab === 'opencode') return null
  if (tab === 'claude') {
    if (isOpenAIMessagesDispatchClaudeTab.value) return 'openai'
    return 'anthropic'
  }
  if (tab === 'gemini') return 'gemini'
  if (tab === 'codex' || tab === 'codex-ws') return 'openai'
  if (tab === 'curl' || tab === 'python') {
    if (props.routingMode === 'universal') return 'openai'
    switch (props.platform) {
      case 'openai':
      case 'newapi':
      case 'grok':
        return 'openai'
      case 'gemini':
      case 'antigravity':
        return 'gemini'
      default:
        return 'anthropic'
    }
  }
  switch (props.platform) {
    case 'gemini':
      return 'gemini'
    case 'antigravity':
      return 'gemini'
    case 'openai':
    case 'newapi':
    case 'grok':
      return 'openai'
    case 'anthropic':
      return 'anthropic'
    default:
      return 'anthropic'
  }
})

const tk = useTkUseKey({
  apiKeyId: toRef(props, 'apiKeyId'),
  apiKey: toRef(props, 'apiKey'),
  platform: toRef(props, 'platform'),
  routingMode: toRef(props, 'routingMode'),
  claudeCodeOnly: toRef(props, 'claudeCodeOnly'),
  baseRoot,
})

// (Re)load the live servable model menu whenever the key changes.
watch(
  () => [props.apiKeyId, props.initialModel] as const,
  async ([id, initialModel]) => {
    if (id == null) return
    keyRevealed.value = false
    tk.testState.value = { status: 'idle' }
    await tk.loadModels()
    const flavor = tk.applyInitialModel(initialModel)
    if (!props.selectedClient) {
      if (flavor === 'anthropic') activeClientTab.value = 'claude'
      else if (flavor === 'gemini') activeClientTab.value = 'gemini'
      else if (flavor === 'openai') activeClientTab.value = 'codex'
    }
  },
  { immediate: true },
)

// Models offered in the picker for the current flavor.
const pickerModels = computed(() => (activeFlavor.value ? tk.modelsForFlavor(activeFlavor.value) : []))
const selectedModel = computed(() => (activeFlavor.value ? tk.effectiveModel(activeFlavor.value) : ''))
watch(selectedModel, (model) => {
  if (model) emit('modelChange', model)
}, { immediate: true })
const showModelsCatalogEmpty = computed(() =>
  activeFlavor.value ? tk.shouldWarnModelsEmpty(activeFlavor.value) : false,
)
const currentModelMeta = computed(() =>
  pickerModels.value.find((m) => m.id === selectedModel.value),
)
const isDifyClient = computed(() => activeClientTab.value === 'dify')
const testErrorMessage = computed(() => tkTestState.value.reason === 'missing_tool_call'
  ? t('quickstart.toolCallMissing')
  : tkTestState.value.message,
)
function onPickModel(e: Event): void {
  const id = (e.target as HTMLSelectElement).value
  if (activeFlavor.value) tk.setModel(activeFlavor.value, id)
}

// Template-facing refs (top-level so they auto-unwrap in the template).
const tkModelsLoading = tk.modelsLoading
const tkTestState = tk.testState
const tkIsCCOnly = tk.isClaudeCodeOnly

const maskedKey = computed(() => {
  const k = props.apiKey || ''
  if (k.length <= 14) return k
  return `${k.slice(0, 6)}${'•'.repeat(16)}${k.slice(-4)}`
})

function copyText(text: string): void {
  void clipboardCopy(text)
}

function toggleExpandedFile(index: number): void {
  expandedFileIndex.value = expandedFileIndex.value === index ? null : index
}

function isCollapsible(file: FileConfig): boolean {
  return file.content.split('\n').length > 16
}

function onTest(): void {
  if (activeFlavor.value) {
    void tk.runTest(activeFlavor.value, { requireToolCall: isDifyClient.value })
  }
}

function formatCtx(n?: number): string {
  if (!n) return ''
  if (n >= 1024 * 1024) return `${Math.round(n / (1024 * 1024))}M ctx`
  if (n >= 1000) return `${Math.round(n / 1000)}k ctx`
  return `${n} ctx`
}

// Reset tabs when platform changes.
// `newapi` (the fifth platform) is an OpenAI-compatible HTTP gateway: the
// upstream speaks OpenAI's /v1/chat/completions shape but does not expose
// ChatGPT WebSocket auth, so codex (HTTP) is the right default — same as
// `openai` minus codex-ws.
// antigravity 的 Claude flavor 是否对当前分组开放：分组 supported_model_scopes 含
// 'claude' 才显示。这是 claude 维度上与后端 /antigravity/v1/models scope 过滤的一致点；
// 本指南不做 gemini_text/gemini_image 的逐模型细分（那只在后端 /models 生效）。
// 空/未传 = 不限制（向后兼容旧分组）。非 antigravity 平台不受影响。
const antigravityClaudeAllowed = computed(() => {
  if (props.platform !== PLATFORM_ANTIGRAVITY) return true
  const scopes = props.supportedModelScopes
  if (!scopes || scopes.length === 0) return true
  return scopes.includes('claude')
})

const defaultClientTab = computed(() => {
  if (props.routingMode === 'universal') return 'claude'
  switch (props.platform) {
    case 'openai':
      return 'codex'
    case 'newapi':
      return 'codex'
    case 'grok':
      return 'codex'
    case 'gemini':
      return 'gemini'
    case 'antigravity':
      return antigravityClaudeAllowed.value ? 'claude' : 'gemini'
    default:
      return 'claude'
  }
})

watch(() => [props.platform, props.routingMode, props.selectedClient, props.selectedTransport] as const, () => {
  activeTab.value = 'unix'
  activeClientTab.value = props.selectedClient === 'codex' && props.selectedTransport === 'websocket'
    ? 'codex-ws'
    : props.selectedClient || defaultClientTab.value
}, { immediate: true })

// Reset shell tab when client changes
watch(activeClientTab, () => {
  activeTab.value = 'unix'
  expandedFileIndex.value = null
})

// Icon components
const AppleIcon = {
  render() {
    return h('svg', {
      fill: 'currentColor',
      viewBox: '0 0 24 24',
      class: 'w-4 h-4'
    }, [
      h('path', { d: 'M18.71 19.5c-.83 1.24-1.71 2.45-3.05 2.47-1.34.03-1.77-.79-3.29-.79-1.53 0-2 .77-3.27.82-1.31.05-2.3-1.32-3.14-2.53C4.25 17 2.94 12.45 4.7 9.39c.87-1.52 2.43-2.48 4.12-2.51 1.28-.02 2.5.87 3.29.87.78 0 2.26-1.07 3.81-.91.65.03 2.47.26 3.64 1.98-.09.06-2.17 1.28-2.15 3.81.03 3.02 2.65 4.03 2.68 4.04-.03.07-.42 1.44-1.38 2.83M13 3.5c.73-.83 1.94-1.46 2.94-1.5.13 1.17-.34 2.35-1.04 3.19-.69.85-1.83 1.51-2.95 1.42-.15-1.15.41-2.35 1.05-3.11z' })
    ])
  }
}

const WindowsIcon = {
  render() {
    return h('svg', {
      fill: 'currentColor',
      viewBox: '0 0 24 24',
      class: 'w-4 h-4'
    }, [
      h('path', { d: 'M3 12V6.75l6-1.32v6.48L3 12zm17-9v8.75l-10 .15V5.21L20 3zM3 13l6 .09v6.81l-6-1.15V13zm7 .25l10 .15V21l-10-1.91v-5.84z' })
    ])
  }
}

// Terminal icon for Claude Code
const TerminalIcon = {
  render() {
    return h('svg', {
      fill: 'none',
      stroke: 'currentColor',
      viewBox: '0 0 24 24',
      'stroke-width': '1.5',
      class: 'w-4 h-4'
    }, [
      h('path', {
        'stroke-linecap': 'round',
        'stroke-linejoin': 'round',
        d: 'm6.75 7.5 3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 17.25V6.75A2.25 2.25 0 0 0 18.75 4.5H5.25A2.25 2.25 0 0 0 3 6.75v10.5A2.25 2.25 0 0 0 5.25 20.25Z'
      })
    ])
  }
}

// Sparkle icon for Gemini
const SparkleIcon = {
  render() {
    return h('svg', {
      fill: 'none',
      stroke: 'currentColor',
      viewBox: '0 0 24 24',
      'stroke-width': '1.5',
      class: 'w-4 h-4'
    }, [
      h('path', {
        'stroke-linecap': 'round',
        'stroke-linejoin': 'round',
        d: 'M9.813 15.904 9 18.75l-.813-2.846a4.5 4.5 0 0 0-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 0 0 3.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 0 0 3.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 0 0-3.09 3.09ZM18.259 8.715 18 9.75l-.259-1.035a3.375 3.375 0 0 0-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 0 0 2.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 0 0 2.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 0 0-2.456 2.456ZM16.894 20.567 16.5 21.75l-.394-1.183a2.25 2.25 0 0 0-1.423-1.423L13.5 18.75l1.183-.394a2.25 2.25 0 0 0 1.423-1.423l.394-1.183.394 1.183a2.25 2.25 0 0 0 1.423 1.423l1.183.394-1.183.394a2.25 2.25 0 0 0-1.423 1.423Z'
      })
    ])
  }
}

// Raw-protocol tabs (cURL + Python). Data-driven: a large share of #1 auth,
// #4 malformed-body and #5 wrong-endpoint errors come from Python/curl callers
// hand-building requests — give them a fully-injected, correct example.
const rawProtoTabs = (): TabConfig[] => [
  { id: 'curl', label: t('keys.useKeyModal.cliTabs.curl'), icon: TerminalIcon },
  { id: 'python', label: t('keys.useKeyModal.cliTabs.python'), icon: TerminalIcon },
]

/** Resolve platform for snippet generation; universal keys derive from active tab. */
function platformForFiles(): GroupPlatform | null {
  if (props.routingMode === 'universal') {
    const tab = activeClientTab.value
    if (tab === 'gemini') return PLATFORM_GEMINI
    if (selectedClientEntry.value?.guideMode === 'qwen') {
      return props.selectedProtocol === 'openai' ? PLATFORM_OPENAI : PLATFORM_ANTHROPIC
    }
    if (tab === 'codex' || tab === 'codex-ws' || selectedClientEntry.value?.guideMode === 'raw'
      || selectedClientEntry.value?.guideMode === 'openai-fields' || tab === 'opencode') {
      return PLATFORM_OPENAI
    }
    return PLATFORM_ANTHROPIC
  }
  return props.platform
}

const clientTabs = computed((): TabConfig[] => {
  if (!hasGuideContext.value) return []
  if (props.routingMode === 'universal') {
    return [
      { id: 'claude', label: t('keys.useKeyModal.cliTabs.claudeCode'), icon: TerminalIcon },
      { id: 'codex', label: t('keys.useKeyModal.cliTabs.codexCli'), icon: TerminalIcon },
      { id: 'codex-ws', label: t('keys.useKeyModal.cliTabs.codexCliWs'), icon: TerminalIcon },
      { id: 'gemini', label: t('keys.useKeyModal.cliTabs.geminiCli'), icon: SparkleIcon },
      ...rawProtoTabs(),
      { id: 'opencode', label: t('keys.useKeyModal.cliTabs.opencode'), icon: TerminalIcon },
    ]
  }
  switch (props.platform) {
    case 'openai': {
      const tabs: TabConfig[] = [
        { id: 'codex', label: t('keys.useKeyModal.cliTabs.codexCli'), icon: TerminalIcon },
        { id: 'codex-ws', label: t('keys.useKeyModal.cliTabs.codexCliWs'), icon: TerminalIcon },
      ]
      if (props.allowMessagesDispatch) {
        tabs.push({ id: 'claude', label: t('keys.useKeyModal.cliTabs.claudeCode'), icon: TerminalIcon })
      }
      tabs.push(...rawProtoTabs())
      tabs.push({ id: 'opencode', label: t('keys.useKeyModal.cliTabs.opencode'), icon: TerminalIcon })
      return tabs
    }
    case 'gemini':
      return [
        { id: 'gemini', label: t('keys.useKeyModal.cliTabs.geminiCli'), icon: SparkleIcon },
        ...rawProtoTabs(),
        { id: 'opencode', label: t('keys.useKeyModal.cliTabs.opencode'), icon: TerminalIcon }
      ]
    case 'antigravity': {
      const tabs: TabConfig[] = []
      // supported_model_scopes 不含 claude 时隐藏 Claude Code tab。
      if (antigravityClaudeAllowed.value) {
        tabs.push({ id: 'claude', label: t('keys.useKeyModal.cliTabs.claudeCode'), icon: TerminalIcon })
      }
      tabs.push({ id: 'gemini', label: t('keys.useKeyModal.cliTabs.geminiCli'), icon: SparkleIcon })
      tabs.push(...rawProtoTabs()) // gemini-flavor raw calls (/antigravity/v1beta)
      tabs.push({ id: 'opencode', label: t('keys.useKeyModal.cliTabs.opencode'), icon: TerminalIcon })
      return tabs
    }
    case 'newapi':
    case 'grok': {
      // OpenAI-compat HTTP only; no codex-ws. Optionally claude tab when the
      // group enables messages dispatch (mirrors the openai branch). grok (xAI)
      // shares this flavor: api.x.ai is OpenAI-compatible.
      const tabs: TabConfig[] = [
        { id: 'codex', label: t('keys.useKeyModal.cliTabs.codexCli'), icon: TerminalIcon },
      ]
      if (props.allowMessagesDispatch) {
        tabs.push({ id: 'claude', label: t('keys.useKeyModal.cliTabs.claudeCode'), icon: TerminalIcon })
      }
      tabs.push(...rawProtoTabs())
      tabs.push({ id: 'opencode', label: t('keys.useKeyModal.cliTabs.opencode'), icon: TerminalIcon })
      return tabs
    }
    default:
      // anthropic. CC-only groups (group.claude_code_only) reject curl/python/
      // opencode at the gateway with a 403 ("use claude-cli") — the #3 error
      // bucket (~414/wk). So we offer ONLY Claude Code there, no foot-guns.
      if (tk.isClaudeCodeOnly.value) {
        return [{ id: 'claude', label: t('keys.useKeyModal.cliTabs.claudeCode'), icon: TerminalIcon }]
      }
      return [
        { id: 'claude', label: t('keys.useKeyModal.cliTabs.claudeCode'), icon: TerminalIcon },
        ...rawProtoTabs(),
        { id: 'opencode', label: t('keys.useKeyModal.cliTabs.opencode'), icon: TerminalIcon }
      ]
  }
})

// Shell tabs (3 types for environment variable based configs)
const shellTabs: TabConfig[] = [
  { id: 'unix', label: 'macOS / Linux', icon: AppleIcon },
  { id: 'cmd', label: 'Windows CMD', icon: WindowsIcon },
  { id: 'powershell', label: 'PowerShell', icon: WindowsIcon }
]

// OpenAI tabs (2 OS types)
const openaiTabs: TabConfig[] = [
  { id: 'unix', label: 'macOS / Linux', icon: AppleIcon },
  { id: 'windows', label: 'Windows', icon: WindowsIcon }
]

const showShellTabs = computed(() => selectedClientEntry.value
  ? selectedClientEntry.value.usesEnvironmentPicker === true
  : ['claude', 'codex', 'codex-ws', 'gemini'].includes(activeClientTab.value),
)

const currentTabs = computed(() => {
  if (!showShellTabs.value) return []
  if (activeClientTab.value === 'codex' || activeClientTab.value === 'codex-ws') {
    return openaiTabs
  }
  return shellTabs
})

// Treat newapi as openai for description / note copy: the user-facing client
// instructions are identical (codex CLI + opencode), and our gateway already
// routes both platforms through the OpenAI-compat handlers.
const isOpenAICompatPlatform = computed(() => {
  const p = platformForFiles()
  return p === PLATFORM_OPENAI || p === PLATFORM_NEWAPI || p === PLATFORM_GROK
})

const platformDescription = computed(() => {
  if (isOpenAICompatPlatform.value) {
    if (activeClientTab.value === 'claude') {
      return t('keys.useKeyModal.description')
    }
    return t('keys.useKeyModal.openai.description')
  }
  switch (platformForFiles()) {
    case 'gemini':
      return t('keys.useKeyModal.gemini.description')
    case 'antigravity':
      return t('keys.useKeyModal.antigravity.description')
    default:
      return t('keys.useKeyModal.description')
  }
})

const platformNote = computed(() => {
  if (selectedClientEntry.value) {
    return t('quickstart.clientConfigNote')
  }
  if (isOpenAICompatPlatform.value) {
    if (activeClientTab.value === 'claude') {
      return t('keys.useKeyModal.note')
    }
    return activeTab.value === 'windows'
      ? t('keys.useKeyModal.openai.noteWindows')
      : t('keys.useKeyModal.openai.note')
  }
  switch (platformForFiles()) {
    case 'gemini':
      return t('keys.useKeyModal.gemini.note')
    case 'antigravity':
      return activeClientTab.value === 'claude'
        ? t('keys.useKeyModal.antigravity.claudeNote')
        : t('keys.useKeyModal.antigravity.geminiNote')
    default:
      return t('keys.useKeyModal.note')
  }
})

const showPlatformNote = computed(() => activeClientTab.value !== 'opencode')

const escapeHtml = (value: string) => value
  .replace(/&/g, '&amp;')
  .replace(/</g, '&lt;')
  .replace(/>/g, '&gt;')
  .replace(/"/g, '&quot;')
  .replace(/'/g, '&#39;')

const wrapToken = (className: string, value: string) =>
  `<span class="${className}">${escapeHtml(value)}</span>`

const keyword = (value: string) => wrapToken('text-emerald-300', value)
const variable = (value: string) => wrapToken('text-sky-200', value)
const operator = (value: string) => wrapToken('text-slate-400', value)
const string = (value: string) => wrapToken('text-amber-200', value)

// Syntax highlighting helpers
// Generate file configs based on platform and active tab
const currentFiles = computed((): FileConfig[] => {
  const baseUrl = props.baseUrl || window.location.origin
  const apiKey = props.apiKey
  const baseRoot = baseUrl.replace(/\/v1\/?$/, '').replace(/\/+$/, '')
  const ensureV1 = (value: string) => {
    const trimmed = value.replace(/\/+$/, '')
    return trimmed.endsWith('/v1') ? trimmed : `${trimmed}/v1`
  }
  const apiBase = ensureV1(baseRoot)
  const antigravityBase = ensureV1(`${baseRoot}/antigravity`)
  const antigravityGeminiBase = (() => {
    const trimmed = `${baseRoot}/antigravity`.replace(/\/+$/, '')
    return trimmed.endsWith('/v1beta') ? trimmed : `${trimmed}/v1beta`
  })()
  const geminiBase = (() => {
    const trimmed = baseRoot.replace(/\/+$/, '')
    return trimmed.endsWith('/v1beta') ? trimmed : `${trimmed}/v1beta`
  })()

  // The picker-selected model for the active flavor — injected into every
  // single-model snippet so a real, servable id replaces the old hardcoded
  // literal / free-text hint.
  const model = selectedModel.value

  if (selectedClientEntry.value?.guideMode === 'qwen') {
    const qwenBase = props.routingMode !== 'universal'
      && props.platform === PLATFORM_ANTIGRAVITY
      && props.selectedProtocol !== 'openai'
      ? antigravityBase
      : apiBase
    return generateQwenCodeFiles(
      qwenBase,
      apiKey,
      model,
      props.selectedProtocol === 'openai' ? 'openai' : 'anthropic',
      activeTab.value,
    )
  }

  if (selectedClientEntry.value?.guideMode === 'openai-fields') {
    return generateCompatibleClientFields(activeClientTab.value, baseRoot, apiKey, model, {
      quota: props.keyQuota,
      rate5h: props.rateLimit5h,
      rate1d: props.rateLimit1d,
      rate7d: props.rateLimit7d,
    })
  }

  if (activeClientTab.value === 'opencode') {
    const allowedModels = tk.modelsLoaded.value ? tk.servableModels.value : undefined
    const splitAllowedModels = props.routingMode === 'universal'
    switch (platformForFiles()) {
      case 'anthropic':
        return [generateOpenCodeConfig('anthropic', apiBase, apiKey, undefined, allowedModels, splitAllowedModels)]
      case 'openai':
      case 'newapi':
      case 'grok':
        // newapi/grok share the OpenAI-compat HTTP shape: codex CLI / opencode
        // use identical config (provider=openai, baseURL=apiBase). Sticking
        // these branches together avoids a parallel catalog that would drift
        // from openai's.
        return [generateOpenCodeConfig('openai', apiBase, apiKey, undefined, allowedModels, splitAllowedModels)]
      case 'gemini':
        return [generateOpenCodeConfig('gemini', geminiBase, apiKey, undefined, allowedModels, splitAllowedModels)]
      case 'antigravity': {
        const configs: FileConfig[] = []
        // supported_model_scopes 不含 claude 时隐藏 antigravity-claude provider。
        if (antigravityClaudeAllowed.value) {
          configs.push(generateOpenCodeConfig('antigravity-claude', antigravityBase, apiKey, 'opencode.json (Claude)', allowedModels))
        }
        configs.push(generateOpenCodeConfig('antigravity-gemini', antigravityGeminiBase, apiKey, 'opencode.json (Gemini)', allowedModels))
        return configs
      }
      default:
        return [generateOpenCodeConfig('openai', apiBase, apiKey, undefined, allowedModels)]
    }
  }

  // Raw protocol (cURL / Python): one fully-injected, correct example per
  // flavor. base / auth / endpoint / body shape are all correct-by-construction.
  if (selectedClientEntry.value?.guideMode === 'raw'
    || activeClientTab.value === 'curl'
    || activeClientTab.value === 'python') {
    const flavor = activeFlavor.value ?? 'anthropic'
    const isAntigravity = platformForFiles() === PLATFORM_ANTIGRAVITY
    return activeClientTab.value === 'curl'
      ? [generateCurl(flavor, baseRoot, apiKey, model, isAntigravity)]
      : [generatePython(flavor, baseRoot, apiKey, model, isAntigravity)]
  }

  switch (platformForFiles()) {
    case 'openai':
      if (activeClientTab.value === 'claude') {
        return generateAnthropicFiles(baseUrl, apiKey, model, isOpenAIMessagesDispatchClaudeTab.value)
      }
      if (activeClientTab.value === 'codex-ws') {
        return generateOpenAIWsFiles(baseUrl, apiKey, model)
      }
      return generateOpenAIFiles(baseUrl, apiKey, model)
    case 'newapi':
    case 'grok':
      // newapi/grok: OpenAI-compat HTTP, no OAuth WS path (codex-ws not offered
      // in their tabs). claude tab only appears when the group enables messages
      // dispatch. grok (xAI) is OpenAI-compatible, so it shares the openai files.
      if (activeClientTab.value === 'claude') {
        return generateAnthropicFiles(baseUrl, apiKey, model, isOpenAIMessagesDispatchClaudeTab.value)
      }
      return generateOpenAIFiles(baseUrl, apiKey, model)
    case 'gemini':
      return [generateGeminiCliContent(baseUrl, apiKey, model)]
    case 'antigravity':
      if (activeClientTab.value === PLATFORM_GEMINI) {
        return [generateGeminiCliContent(`${baseUrl}/antigravity`, apiKey, model)]
      }
      return generateAnthropicFiles(`${baseUrl}/antigravity`, apiKey, model)
    default:
      return generateAnthropicFiles(baseUrl, apiKey, model)
  }
})

function generateAnthropicFiles(
  baseUrl: string,
  apiKey: string,
  model: string,
  openaiMessagesDispatch = false,
): FileConfig[] {
  const envModel = claudeCodeEnvModel(model, { openaiMessagesDispatch })
  // Picker-injected model, with the 1M-window [1m] alias re-applied:
  //   - Anthropic direct: claude-opus-4-8 → claude-opus-4-8[1m]
  //   - OpenAI messages dispatch: gpt-5.5 → gpt-5.5[1m]
  // Gateway strips the suffix before upstream forward while CC keeps the 1M client window.
  //   - model claude-opus-4-8[1m] + DISABLE_ADAPTIVE_THINKING + fixed MAX_THINKING_TOKENS: 稳定思考预算
  //     NOTE: the model id MUST be a real, empirically-servable Anthropic id
  //     (see backend supportedAnthropicCatalogModels, regenerated from a live
  //     prod probe). A bare alias like `opus` is NOT servable: the gateway
  //     strips the trailing `[1m]` context-window suffix (gateway_anthropic_
  //     context_window_alias_tk.go) so `opus[1m]` collapses to `opus`, which is
  //     rejected upstream as model-not-found and surfaced as 400 invalid_request
  //     (PR #617). `claude-opus-4-8[1m]` collapses to the servable
  //     `claude-opus-4-8` while the separate `context-1m-2025-08-07` beta header
  //     still activates the 1M window.
  //   - CLAUDE_CODE_AUTOCOMPACT_PCT_OVERRIDE=60: 约 60% 上下文占用时触发自动压缩（1M 窗口下更稳）
  //   - 不默认开 DISABLE_NONESSENTIAL_TRAFFIC: 直连 Anthropic 时它会把 cache TTL 从 1h 砍到 5min
  let path: string
  let content: string

  switch (activeTab.value) {
    case 'unix':
      path = 'Terminal'
      content = `export ANTHROPIC_BASE_URL="${baseUrl}"
export ANTHROPIC_AUTH_TOKEN="${apiKey}"
export ANTHROPIC_MODEL="${envModel}"

# 防降智 + 控成本（详见 hint）
export CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1
export MAX_THINKING_TOKENS=31999
export CLAUDE_CODE_AUTOCOMPACT_PCT_OVERRIDE=60
export CLAUDE_CODE_ATTRIBUTION_HEADER=0

# 仅当上游为 Anthropic OAuth 且确实不想上传 telemetry 时再开（会损害 cache TTL）：
# export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
# 谨慎模式（部分场景慢 30%）：
# export CLAUDE_CODE_MAKE_NO_MISTAKES=1`
      break
    case 'cmd':
      path = 'Command Prompt'
      content = `set ANTHROPIC_BASE_URL=${baseUrl}
set ANTHROPIC_AUTH_TOKEN=${apiKey}
set ANTHROPIC_MODEL=${envModel}

set CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1
set MAX_THINKING_TOKENS=31999
set CLAUDE_CODE_AUTOCOMPACT_PCT_OVERRIDE=60
set CLAUDE_CODE_ATTRIBUTION_HEADER=0

REM 仅当上游为 Anthropic OAuth 时再开（会损害 cache TTL）：
REM set CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
REM 谨慎模式：
REM set CLAUDE_CODE_MAKE_NO_MISTAKES=1`
      break
    case 'powershell':
      path = 'PowerShell'
      content = `$env:ANTHROPIC_BASE_URL="${baseUrl}"
$env:ANTHROPIC_AUTH_TOKEN="${apiKey}"
$env:ANTHROPIC_MODEL="${envModel}"

$env:CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING="1"
$env:MAX_THINKING_TOKENS="31999"
$env:CLAUDE_CODE_AUTOCOMPACT_PCT_OVERRIDE="60"
$env:CLAUDE_CODE_ATTRIBUTION_HEADER="0"

# 仅当上游为 Anthropic OAuth 时再开（会损害 cache TTL）：
# $env:CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1"
# 谨慎模式：
# $env:CLAUDE_CODE_MAKE_NO_MISTAKES="1"`
      break
    default:
      path = 'Terminal'
      content = ''
  }

  const vscodeSettingsPath = activeTab.value === 'unix'
    ? '~/.claude/settings.json'
    : '%userprofile%\\.claude\\settings.json'

  const vscodeContent = `{
  "model": "${envModel}",
  "effortLevel": "high",
  "env": {
    "ANTHROPIC_BASE_URL": "${baseUrl}",
    "ANTHROPIC_AUTH_TOKEN": "${apiKey}",
    "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING": "1",
    "MAX_THINKING_TOKENS": "31999",
    "CLAUDE_CODE_AUTOCOMPACT_PCT_OVERRIDE": "60",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0"
  }
}`

  return [
    { path, content, hint: t('keys.useKeyModal.claudeCode.envHint') },
    { path: vscodeSettingsPath, content: vscodeContent, hint: t('keys.useKeyModal.claudeCode.vscodeHint') }
  ]
}

function generateGeminiCliContent(baseUrl: string, apiKey: string, model: string): FileConfig {
  let path: string
  let content: string
  let highlighted: string

  switch (activeTab.value) {
    case 'unix':
      path = 'Terminal'
      content = `export GOOGLE_GEMINI_BASE_URL="${baseUrl}"
export GEMINI_API_KEY="${apiKey}"
export GEMINI_MODEL="${model}"`
      highlighted = `${keyword('export')} ${variable('GOOGLE_GEMINI_BASE_URL')}${operator('=')}${string(`"${baseUrl}"`)}
${keyword('export')} ${variable('GEMINI_API_KEY')}${operator('=')}${string(`"${apiKey}"`)}
${keyword('export')} ${variable('GEMINI_MODEL')}${operator('=')}${string(`"${model}"`)}`
      break
    case 'cmd':
      path = 'Command Prompt'
      content = `set GOOGLE_GEMINI_BASE_URL=${baseUrl}
set GEMINI_API_KEY=${apiKey}
set GEMINI_MODEL=${model}`
      highlighted = `${keyword('set')} ${variable('GOOGLE_GEMINI_BASE_URL')}${operator('=')}${string(baseUrl)}
${keyword('set')} ${variable('GEMINI_API_KEY')}${operator('=')}${string(apiKey)}
${keyword('set')} ${variable('GEMINI_MODEL')}${operator('=')}${string(model)}`
      break
    case 'powershell':
      path = 'PowerShell'
      content = `$env:GOOGLE_GEMINI_BASE_URL="${baseUrl}"
$env:GEMINI_API_KEY="${apiKey}"
$env:GEMINI_MODEL="${model}"`
      highlighted = `${keyword('$env:')}${variable('GOOGLE_GEMINI_BASE_URL')}${operator('=')}${string(`"${baseUrl}"`)}
${keyword('$env:')}${variable('GEMINI_API_KEY')}${operator('=')}${string(`"${apiKey}"`)}
${keyword('$env:')}${variable('GEMINI_MODEL')}${operator('=')}${string(`"${model}"`)}`
      break
    default:
      path = 'Terminal'
      content = ''
      highlighted = ''
  }

  return { path, content, highlighted }
}

function generateOpenAIFiles(baseUrl: string, apiKey: string, model: string): FileConfig[] {
  const isWindows = activeTab.value === 'windows'
  const configDir = isWindows ? '%userprofile%\\.codex' : '~/.codex'

  // config.toml content
  const configContent = `model_provider = "OpenAI"
model = "${model}"
review_model = "${model}"
model_reasoning_effort = "xhigh"
disable_response_storage = true
network_access = "enabled"
windows_wsl_setup_acknowledged = true

[model_providers.OpenAI]
name = "OpenAI"
base_url = "${baseUrl}"
wire_api = "responses"
requires_openai_auth = true

[features]
goals = true`

  // auth.json content
  const authContent = `{
  "OPENAI_API_KEY": "${apiKey}"
}`

  return [
    {
      path: `${configDir}/config.toml`,
      content: configContent,
      hint: t('keys.useKeyModal.openai.configTomlHint')
    },
    {
      path: `${configDir}/auth.json`,
      content: authContent
    }
  ]
}

function generateOpenAIWsFiles(baseUrl: string, apiKey: string, model: string): FileConfig[] {
  const isWindows = activeTab.value === 'windows'
  const configDir = isWindows ? '%userprofile%\\.codex' : '~/.codex'

  // config.toml content with WebSocket v2
  const configContent = `model_provider = "OpenAI"
model = "${model}"
review_model = "${model}"
model_reasoning_effort = "xhigh"
disable_response_storage = true
network_access = "enabled"
windows_wsl_setup_acknowledged = true

[model_providers.OpenAI]
name = "OpenAI"
base_url = "${baseUrl}"
wire_api = "responses"
supports_websockets = true
requires_openai_auth = true

[features]
responses_websockets_v2 = true
goals = true`

  // auth.json content
  const authContent = `{
  "OPENAI_API_KEY": "${apiKey}"
}`

  return [
    {
      path: `${configDir}/config.toml`,
      content: configContent,
      hint: t('keys.useKeyModal.openai.configTomlHint')
    },
    {
      path: `${configDir}/auth.json`,
      content: authContent
    }
  ]
}

function generateQwenCodeFiles(
  apiBase: string,
  apiKey: string,
  model: string,
  protocol: 'anthropic' | 'openai',
  os: string,
): FileConfig[] {
  const isWindows = os !== 'unix'
  const root = isWindows ? '%USERPROFILE%\\.qwen' : '~/.qwen'
  const settings = {
    security: { auth: { selectedType: protocol } },
    model: { name: model },
    modelProviders: {
      [protocol]: [{
        id: model,
        name: model,
        envKey: 'TOKENKEY_API_KEY',
        baseUrl: apiBase,
      }],
    },
  }

  return [
    {
      path: `${root}/.env`,
      content: `TOKENKEY_API_KEY=${apiKey}`,
      hint: t('quickstart.qwenSecretHint'),
    },
    {
      path: `${root}/settings.json`,
      content: JSON.stringify(settings, null, 2),
    },
  ]
}

function generateCompatibleClientFields(
  client: string,
  baseRoot: string,
  apiKey: string,
  model: string,
  limits: { quota?: number; rate5h?: number; rate1d?: number; rate7d?: number },
): FileConfig[] {
  const apiBase = `${baseRoot.replace(/\/+$/, '')}/v1`
  const common = [
    `Base URL: ${apiBase}`,
    `API Key: ${apiKey}`,
    `Model ID: ${model}`,
  ]
  let fields: string[]

  switch (client) {
    case 'dify':
      fields = [
        'Provider: OpenAI-API-compatible',
        `Model Name: ${model}`,
        `API Key: ${apiKey}`,
        `API Base URL: ${apiBase}`,
        'Mode: Chat',
        'Function Call Type: Tool Call',
      ]
      break
    case 'chatbox':
      fields = [
        'Provider: Custom Provider',
        'API mode: OpenAI API Compatible',
        `API Host: ${baseRoot}`,
        'API Path: /v1/chat/completions',
        `API Key: ${apiKey}`,
        `Model: ${model}`,
      ]
      break
    case 'cherry-studio':
      fields = ['Provider Type: OpenAI Compatible', ...common]
      break
    case 'lobe-chat':
      fields = ['Provider: OpenAI', ...common]
      break
    default:
      fields = ['API Provider: OpenAI Compatible', ...common]
      break
  }

  const files: FileConfig[] = [{
    path: t('quickstart.configFields'),
    content: fields.join('\n'),
    hint: t('quickstart.secretUiHint'),
  }]
  if (client === 'dify') {
    const showLimit = (value?: number) => value && value > 0 ? `$${value}` : t('quickstart.unlimited')
    files.push({
      path: t('quickstart.limitReference'),
      content: [
        `${t('quickstart.keyQuota')}: ${showLimit(limits.quota)}`,
        `${t('quickstart.limit5h')}: ${showLimit(limits.rate5h)}`,
        `${t('quickstart.limit1d')}: ${showLimit(limits.rate1d)}`,
        `${t('quickstart.limit7d')}: ${showLimit(limits.rate7d)}`,
      ].join('\n'),
      hint: t('quickstart.difyLimitHint'),
    })
  }
  return files
}

// Raw-protocol snippets: a complete, runnable request with model / base_url /
// auth-header / body all injected correct-by-construction. Targets the
// Python/curl callers that dominate the auth (#1), malformed-body (#4) and
// wrong-endpoint (#5) error buckets.
function generateCurl(
  flavor: UseKeyFlavor,
  baseRoot: string,
  apiKey: string,
  model: string,
  isAntigravity: boolean,
): FileConfig {
  const agPrefix = isAntigravity ? '/antigravity' : ''
  if (flavor === PLATFORM_ANTHROPIC) {
    const envModel = anthropicEnvModel(model)
    return {
      path: 'cURL',
      content: `curl ${baseRoot}${agPrefix}/v1/messages \\
  -H "x-api-key: ${apiKey}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "content-type: application/json" \\
  -d '{
    "model": "${envModel}",
    "max_tokens": 64,
    "messages": [{"role": "user", "content": "Hello"}]
  }'`,
    }
  }
  if (flavor === PLATFORM_GEMINI) {
    return {
      path: 'cURL',
      content: `curl "${baseRoot}${agPrefix}/v1beta/models/${model}:generateContent" \\
  -H "x-goog-api-key: ${apiKey}" \\
  -H "content-type: application/json" \\
  -d '{
    "contents": [{"role": "user", "parts": [{"text": "Hello"}]}]
  }'`,
    }
  }
  return {
    path: 'cURL',
    content: `curl ${baseRoot}/v1/chat/completions \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 64
  }'`,
  }
}

function generatePython(
  flavor: UseKeyFlavor,
  baseRoot: string,
  apiKey: string,
  model: string,
  isAntigravity: boolean,
): FileConfig {
  const agPrefix = isAntigravity ? '/antigravity' : ''
  if (flavor === PLATFORM_ANTHROPIC) {
    const envModel = anthropicEnvModel(model)
    return {
      path: 'Python (anthropic SDK)',
      content: `from anthropic import Anthropic

client = Anthropic(api_key="${apiKey}", base_url="${baseRoot}${agPrefix}")
msg = client.messages.create(
    model="${envModel}",
    max_tokens=64,
    messages=[{"role": "user", "content": "Hello"}],
)
print(msg.content[0].text)`,
    }
  }
  if (flavor === PLATFORM_GEMINI) {
    return {
      path: 'Python (requests)',
      content: `import requests

resp = requests.post(
    "${baseRoot}${agPrefix}/v1beta/models/${model}:generateContent",
    headers={"x-goog-api-key": "${apiKey}", "Content-Type": "application/json"},
    json={"contents": [{"role": "user", "parts": [{"text": "Hello"}]}]},
)
print(resp.json())`,
    }
  }
  return {
    path: 'Python (openai SDK)',
    content: `from openai import OpenAI

client = OpenAI(api_key="${apiKey}", base_url="${baseRoot}/v1")   # <- 改这两行
resp = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "Hello"}],
    max_tokens=64,
)
print(resp.choices[0].message.content)`,
  }
}

function generateOpenCodeConfig(
  platform: string,
  baseUrl: string,
  apiKey: string,
  pathLabel?: string,
  allowedModels?: UseKeyServableModel[],
  filterAllowedByFlavor = true,
): FileConfig {
  const provider: Record<string, any> = {
    [platform]: {
      options: {
        baseURL: baseUrl,
        apiKey
      }
    }
  }
  // NOTE: keep this catalog aligned with the empirically-servable OpenAI set
  // (backend supportedOpenAICatalogModels, regenerated from a live prod probe —
  // PR #608). gpt-5.2 was dropped here because the probe returned 502 on the
  // chat/completions path opencode uses and it is retired from the servable
  // allowlist + public catalog + Your Menu. codex-mini-latest is intentionally
  // LEFT for now: it carries a codex-ws ChatGPT-OAuth reverse-mapping
  // (normalizeOpenAIModelForUpstream, scripts/sentinels/gateway-tk.json).
  // gpt-5.3-codex is a non-display alias to spark and is not shown here.
  const openaiModels = {
    'gpt-5.5': {
      name: 'GPT-5.5',
      limit: {
        context: 1050000,
        output: 128000
      },
      options: {
        store: false
      },
      variants: {
        low: {},
        medium: {},
        high: {},
        xhigh: {}
      }
    },
    'gpt-5.4': {
      name: 'GPT-5.4',
      limit: {
        context: 1050000,
        output: 128000
      },
      options: {
        store: false
      },
      variants: {
        low: {},
        medium: {},
        high: {},
        xhigh: {}
      }
    },
    'gpt-5.4-mini': {
      name: 'GPT-5.4 Mini',
      limit: {
        context: 400000,
        output: 128000
      },
      options: {
        store: false
      },
      variants: {
        low: {},
        medium: {},
        high: {},
        xhigh: {}
      }
    },
    'gpt-5.3-codex-spark': {
      name: 'GPT-5.3 Codex Spark',
      limit: {
        context: 128000,
        output: 32000
      },
      options: {
        store: false
      },
      variants: {
        low: {},
        medium: {},
        high: {},
        xhigh: {}
      }
    },
    'codex-mini-latest': {
      name: 'Codex Mini',
      limit: {
        context: 200000,
        output: 100000
      },
      options: {
        store: false
      },
      variants: {
        low: {},
        medium: {},
        high: {}
      }
    }
  }
  const geminiModels = {
    'gemini-2.0-flash': {
      name: 'Gemini 2.0 Flash',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      }
    },
    'gemini-2.5-flash': {
      name: 'Gemini 2.5 Flash',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      }
    },
    'gemini-2.5-pro': {
      name: 'Gemini 2.5 Pro',
      limit: {
        context: 2097152,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-3.5-flash': {
      name: 'Gemini 3.5 Flash',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      }
    },
    'gemini-3-flash-preview': {
      name: 'Gemini 3 Flash Preview',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      }
    },
    'gemini-3-pro-preview': {
      name: 'Gemini 3 Pro Preview',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-3.1-pro-preview': {
      name: 'Gemini 3.1 Pro Preview',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    }
  }

  const antigravityGeminiModels = {
    // 2026-06 实测 /v1internal:fetchAvailableModels 的当前 user-facing wire id（app 下拉显示名见 name）
    'gemini-3.5-flash-low': {
      name: 'Gemini 3.5 Flash (Medium)',
      limit: { context: 1048576, output: 65536 },
      modalities: { input: ['text', 'image', 'pdf'], output: ['text'] },
      options: { thinking: { budgetTokens: 4000, type: 'enabled' } }
    },
    'gemini-3-flash-agent': {
      name: 'Gemini 3.5 Flash (High)',
      limit: { context: 1048576, output: 65536 },
      modalities: { input: ['text', 'image', 'pdf'], output: ['text'] },
      options: { thinking: { budgetTokens: 10000, type: 'enabled' } }
    },
    'gemini-3.5-flash-extra-low': {
      name: 'Gemini 3.5 Flash (Low)',
      limit: { context: 1048576, output: 65536 },
      modalities: { input: ['text', 'image', 'pdf'], output: ['text'] },
      options: { thinking: { budgetTokens: 1000, type: 'enabled' } }
    },
    'gemini-pro-agent': {
      name: 'Gemini 3.1 Pro (High)',
      limit: { context: 1048576, output: 65536 },
      modalities: { input: ['text', 'image', 'pdf'], output: ['text'] },
      options: { thinking: { budgetTokens: 10001, type: 'enabled' } }
    },
    'gemini-2.5-flash': {
      name: 'Gemini 2.5 Flash',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'disable'
        }
      }
    },
    'gemini-2.5-flash-lite': {
      name: 'Gemini 2.5 Flash Lite',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-2.5-flash-thinking': {
      name: 'Gemini 2.5 Flash (Thinking)',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-3-flash': {
      name: 'Gemini 3 Flash',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-3.1-pro-low': {
      name: 'Gemini 3.1 Pro Low',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-3.1-pro-high': {
      name: 'Gemini 3.1 Pro High',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-2.5-flash-image': {
      name: 'Gemini 2.5 Flash Image',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image'],
        output: ['image']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'gemini-3.1-flash-image': {
      name: 'Gemini 3.1 Flash Image',
      limit: {
        context: 1048576,
        output: 65536
      },
      modalities: {
        input: ['text', 'image'],
        output: ['image']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    }
  }
  const claudeModels = {
    'claude-fable-5': {
      name: 'Claude Fable 5',
      limit: {
        context: 1048576,
        output: 128000
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          type: 'adaptive'
        }
      }
    },
    'claude-opus-4-6-thinking': {
      name: 'Claude 4.6 Opus (Thinking)',
      limit: {
        context: 200000,
        output: 128000
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    },
    'claude-sonnet-4-6': {
      name: 'Claude 4.6 Sonnet',
      limit: {
        context: 200000,
        output: 64000
      },
      modalities: {
        input: ['text', 'image', 'pdf'],
        output: ['text']
      },
      options: {
        thinking: {
          budgetTokens: 24576,
          type: 'enabled'
        }
      }
    }
  }

  function restrictModels(
    catalog: Record<string, Record<string, unknown>>,
    flavor: UseKeyFlavor,
  ): Record<string, Record<string, unknown>> {
    if (allowedModels === undefined) return catalog
    return Object.fromEntries(
      allowedModels
        .filter((model) => !filterAllowedByFlavor || flavorOfModel(model.id) === flavor)
        .map((model) => {
          const known = catalog[model.id]
          if (known) return [model.id, known]
          const limit = {
            ...(model.contextWindow ? { context: model.contextWindow } : {}),
            ...(model.maxOutput ? { output: model.maxOutput } : {}),
          }
          return [model.id, {
            name: model.id,
            ...(Object.keys(limit).length ? { limit } : {}),
          }]
        }),
    )
  }

  if (platform === PLATFORM_GEMINI) {
    provider[platform].npm = '@ai-sdk/google'
    provider[platform].models = restrictModels(geminiModels, 'gemini')
  } else if (platform === PLATFORM_ANTHROPIC) {
    provider[platform].npm = '@ai-sdk/anthropic'
    if (allowedModels !== undefined) {
      provider[platform].models = restrictModels({}, 'anthropic')
    }
  } else if (platform === 'antigravity-claude') {
    provider[platform].npm = '@ai-sdk/anthropic'
    provider[platform].name = 'Antigravity (Claude)'
    provider[platform].models = restrictModels(claudeModels, 'anthropic')
  } else if (platform === 'antigravity-gemini') {
    provider[platform].npm = '@ai-sdk/google'
    provider[platform].name = 'Antigravity (Gemini)'
    provider[platform].models = restrictModels(antigravityGeminiModels, 'gemini')
  } else if (platform === PLATFORM_OPENAI) {
    provider[platform].models = restrictModels(openaiModels, 'openai')
  }

  const agent =
    platform === PLATFORM_OPENAI
      ? {
          build: {
            options: {
              store: false
            }
          },
          plan: {
            options: {
              store: false
            }
          }
        }
      : undefined

  const content = JSON.stringify(
    {
      provider,
      ...(agent ? { agent } : {}),
      $schema: 'https://opencode.ai/config.json'
    },
    null,
    2
  )

  return {
    path: pathLabel ?? 'opencode.json',
    content,
    hint: t('keys.useKeyModal.opencode.hint')
  }
}

const copyContent = async (content: string, index: number) => {
  const success = await clipboardCopy(content, t('keys.copied'))
  if (success) {
    copiedIndex.value = index
    setTimeout(() => {
      copiedIndex.value = null
    }, 2000)
  }
}
</script>
