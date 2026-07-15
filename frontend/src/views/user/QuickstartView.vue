<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6 py-4">
      <header>
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white">
          {{ t('quickstart.title') }}
        </h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('quickstart.subtitle') }}
        </p>
      </header>

      <section class="border-y border-gray-200 py-5 dark:border-dark-700">
        <div v-if="keysLoading" class="flex items-center justify-center py-6">
          <LoadingSpinner />
        </div>
        <div v-else-if="keysError" class="text-sm text-red-500">{{ keysError }}</div>
        <div v-else-if="!keys.length" class="space-y-4 text-center">
          <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('quickstart.noKeys') }}</p>
          <router-link to="/keys" class="btn btn-primary text-sm">{{ t('quickstart.createKey') }}</router-link>
        </div>
        <div v-else class="space-y-6">
          <div class="flex flex-col gap-3 sm:flex-row sm:items-end">
            <div class="flex-1">
              <label for="quickstart-key" class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t('quickstart.selectKey') }}
              </label>
              <select
                id="quickstart-key"
                v-model="selectedKeyId"
                data-tk="quickstart-key-select"
                class="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-100"
              >
                <option v-for="k in keys" :key="k.id" :value="k.id">
                  {{ k.name }} ({{ maskKey(k.key) }})
                </option>
              </select>
            </div>
            <div v-if="selectedKey" class="sm:pb-0.5">
              <span class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('keys.group') }}</span>
              <div class="mt-1">
                <span
                  v-if="selectedKey.routing_mode === 'universal'"
                  class="inline-flex items-center gap-1 rounded-md bg-primary-50 px-2 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                >
                  {{ t('keys.universalBadge') }}
                </span>
                <GroupBadge
                  v-else-if="selectedKey.group"
                  :name="selectedKey.group.name"
                  :platform="selectedKey.group.platform"
                  :subscription-type="selectedKey.group.subscription_type"
                  :rate-multiplier="selectedKey.group.rate_multiplier"
                  hide-rate-value
                />
                <span v-else class="text-sm text-amber-600 dark:text-amber-400">{{ t('keys.noGroup') }}</span>
              </div>
            </div>
          </div>

          <div v-if="selectedKey" class="space-y-3">
            <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('quickstart.chooseClient') }}
            </h2>
            <QuickstartClientPicker
              :groups="clientGroups"
              :selected-id="selectedClientId"
              @select="selectClient"
            />
          </div>

          <div
            v-if="selectedKey && selectedClient"
            data-tk="quickstart-config-workspace"
            class="border-t border-gray-200 pt-6 dark:border-dark-700"
          >
            <div class="mb-5 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                    {{ selectedClient.name }}
                  </h2>
                  <span
                    :class="supportBadgeClass"
                    class="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs font-medium"
                  >
                    <Icon :name="supportBadgeIcon" size="xs" />
                    {{ supportBadgeLabel }}
                  </span>
                </div>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                  {{ selectedClientDescription }}
                </p>
              </div>
              <div class="flex shrink-0 flex-wrap gap-2">
                <button
                  v-if="selectedClient.action === 'app-deeplink' && selectedClient.template"
                  type="button"
                  data-tk="quickstart-client-import"
                  class="btn btn-primary inline-flex items-center gap-1.5 text-sm"
                  :disabled="Boolean(selectedClientDisabledReason)"
                  @click="openSelectedClient"
                >
                  <Icon name="download" size="sm" />
                  {{ t('quickstart.openAndImport') }}
                </button>
                <a
                  :href="selectedClient.docsUrl"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="btn btn-secondary inline-flex items-center gap-1.5 text-sm"
                >
                  {{ t('quickstart.clientDocs') }}
                  <Icon name="externalLink" size="sm" />
                </a>
              </div>
            </div>

            <div
              v-if="selectedClientDisabledReason"
              data-tk="quickstart-client-unavailable"
              class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200"
            >
              {{ selectedClientDisabledReason }}
            </div>

            <template v-else>
              <div v-if="selectedClient.id === 'qwen-code'" class="mb-4 flex flex-wrap items-center gap-3">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('quickstart.protocol') }}</span>
                <div
                  data-tk="quickstart-protocol-picker"
                  role="group"
                  :aria-label="t('quickstart.protocol')"
                  class="inline-flex rounded-lg border border-gray-200 p-1 dark:border-dark-600"
                >
                  <button
                    v-for="protocol in qwenProtocols"
                    :key="protocol.id"
                    type="button"
                    :data-tk="`quickstart-protocol-${protocol.id}`"
                    :aria-pressed="selectedProtocol === protocol.id"
                    :disabled="protocol.disabled"
                    :title="protocol.disabled ? t('quickstart.unavailableProtocol') : undefined"
                    :class="[
                      selectedProtocol === protocol.id ? selectedOptionClass : idleOptionClass,
                      protocol.disabled ? 'cursor-not-allowed opacity-45' : '',
                    ]"
                    class="rounded-md px-3 py-1.5 text-sm font-medium transition-colors"
                    @click="!protocol.disabled && (selectedProtocol = protocol.id)"
                  >
                    {{ protocol.label }}
                  </button>
                </div>
              </div>

              <div v-if="selectedClient.id === 'codex-cli'" class="mb-4 flex flex-wrap items-center gap-3">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('quickstart.transport') }}</span>
                <div
                  data-tk="quickstart-transport-picker"
                  role="group"
                  :aria-label="t('quickstart.transport')"
                  class="inline-flex rounded-lg border border-gray-200 p-1 dark:border-dark-600"
                >
                  <button
                    v-for="transport in codexTransports"
                    :key="transport.id"
                    type="button"
                    :data-tk="`quickstart-transport-${transport.id}`"
                    :aria-pressed="selectedTransport === transport.id"
                    :disabled="transport.disabled"
                    :title="transport.disabled ? t('quickstart.websocketUnavailable') : undefined"
                    :class="[
                      selectedTransport === transport.id ? selectedOptionClass : idleOptionClass,
                      transport.disabled ? 'cursor-not-allowed opacity-45' : '',
                    ]"
                    class="rounded-md px-3 py-1.5 text-sm font-medium transition-colors"
                    @click="!transport.disabled && (selectedTransport = transport.id)"
                  >
                    {{ transport.label }}
                  </button>
                </div>
              </div>

              <UseKeyGuide
                :api-key="selectedKey.key"
                :api-key-id="selectedKey.id"
                :base-url="baseUrl"
                :platform="selectedKey.group?.platform ?? null"
                :routing-mode="selectedKey.routing_mode"
                :initial-model="initialModelFromQuery"
                :claude-code-only="selectedKey.group?.claude_code_only || false"
                :allow-messages-dispatch="selectedKey.group?.allow_messages_dispatch || false"
                :supported-model-scopes="selectedKey.group?.supported_model_scopes"
                :key-quota="selectedKey.quota"
                :rate-limit5h="selectedKey.rate_limit_5h"
                :rate-limit1d="selectedKey.rate_limit_1d"
                :rate-limit7d="selectedKey.rate_limit_7d"
                :selected-client="selectedClient.guideId"
                :selected-protocol="selectedProtocol"
                :selected-transport="selectedTransport"
                :show-client-tabs="false"
                @model-change="selectedModel = $event"
              />
            </template>
          </div>
        </div>
      </section>

      <div class="flex flex-wrap items-center justify-center gap-4 pb-6">
        <router-link to="/keys" class="btn btn-secondary text-sm">{{ t('quickstart.manageKeys') }}</router-link>
        <router-link to="/pricing" class="btn btn-secondary text-sm">{{ t('quickstart.viewPricing') }}</router-link>
        <router-link to="/studio" class="btn btn-primary text-sm">{{ t('quickstart.tryStudio') }}</router-link>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import * as keysAPI from '@/api/keys'
import type { ApiKey } from '@/types'
import { isUniversalKey } from '@/utils/studioUniversalKey.tk'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import QuickstartClientPicker, { type QuickstartClientGroup } from '@/components/keys/QuickstartClientPicker.vue'
import UseKeyGuide from '@/components/keys/UseKeyGuide.vue'
import {
  resolveTkClientIntegrationUrl,
  TK_QUICKSTART_CLIENTS,
  type TkClientCatalogEntry,
} from '@/constants/clientIntegrations.tk'
import { flavorOfModel } from '@/composables/useTkUseKey'
import {
  PLATFORM_ANTHROPIC,
  PLATFORM_ANTIGRAVITY,
  PLATFORM_GEMINI,
  PLATFORM_GROK,
  PLATFORM_NEWAPI,
  PLATFORM_OPENAI,
} from '@/constants/gatewayPlatforms'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()

const keys = ref<ApiKey[]>([])
const keysLoading = ref(true)
const keysError = ref('')
const selectedKeyId = ref<number | null>(null)
const selectedClientId = ref('')
const selectedProtocol = ref<'anthropic' | 'openai'>('anthropic')
const selectedTransport = ref<'http' | 'websocket'>('http')
const selectedModel = ref('')

const selectedOptionClass = 'bg-primary-600 text-white shadow-sm'
const idleOptionClass = 'text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700'

const qwenProtocols = computed(() => {
  const available = keyProtocols()
  return [
    {
      id: 'anthropic' as const,
      label: t('quickstart.protocolAnthropic'),
      disabled: !available.includes('anthropic'),
    },
    {
      id: 'openai' as const,
      label: t('quickstart.protocolOpenAI'),
      disabled: !available.includes('openai'),
    },
  ]
})

const codexTransports = computed(() => [
  { id: 'http' as const, label: t('quickstart.transportHttp'), disabled: false },
  { id: 'websocket' as const, label: t('quickstart.transportWebSocket'), disabled: !codexWebSocketAvailable() },
])

const baseUrl = computed(() => {
  const raw = appStore.cachedPublicSettings?.api_base_url || window.location.origin
  return raw.replace(/\/+$/, '')
})

const selectedKey = computed(() =>
  keys.value.find((k) => k.id === selectedKeyId.value) ?? null,
)

const selectedClient = computed(() =>
  TK_QUICKSTART_CLIENTS.find((client) => client.id === selectedClientId.value) ?? null,
)

const selectedClientDescription = computed(() =>
  selectedClient.value ? t(`quickstart.clientDescriptions.${selectedClient.value.guideId}`) : '',
)

const supportBadgeLabel = computed(() => {
  switch (selectedClient.value?.supportTier) {
    case 'verified': return t('quickstart.supportVerified')
    case 'import': return t('quickstart.supportImport')
    default: return t('quickstart.supportCompatible')
  }
})

const supportBadgeIcon = computed<'checkCircle' | 'download' | 'link'>(() => {
  switch (selectedClient.value?.supportTier) {
    case 'verified': return 'checkCircle'
    case 'import': return 'download'
    default: return 'link'
  }
})

const supportBadgeClass = computed(() => {
  switch (selectedClient.value?.supportTier) {
    case 'verified': return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/25 dark:text-emerald-300'
    case 'import': return 'bg-primary-50 text-primary-700 dark:bg-primary-900/25 dark:text-primary-300'
    default: return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
})

function maskKey(key: string) {
  if (key.length <= 14) return key
  return `${key.slice(0, 6)}${'•'.repeat(8)}${key.slice(-4)}`
}

function keyProtocols(): Array<'anthropic' | 'openai' | 'gemini'> {
  const key = selectedKey.value
  if (!key) return []
  if (key.routing_mode === 'universal') return ['anthropic', 'openai', 'gemini']
  const platform = key.group?.platform
  if (platform === PLATFORM_ANTHROPIC) return ['anthropic']
  if (platform === PLATFORM_OPENAI || platform === PLATFORM_NEWAPI || platform === PLATFORM_GROK) {
    const protocols: Array<'anthropic' | 'openai'> = ['openai']
    if (key.group?.allow_messages_dispatch) protocols.push('anthropic')
    return protocols
  }
  if (platform === PLATFORM_GEMINI) return ['gemini']
  if (platform === PLATFORM_ANTIGRAVITY) {
    const scopes = key.group?.supported_model_scopes ?? []
    return !scopes.length || scopes.includes('claude') ? ['anthropic', 'gemini'] : ['gemini']
  }
  return []
}

function disabledReasonFor(client: TkClientCatalogEntry, selectedVariant = false): string {
  if (!selectedKey.value?.group && selectedKey.value?.routing_mode !== 'universal') {
    return t('quickstart.unavailableNoGroup')
  }
  if (selectedKey.value?.group?.claude_code_only && client.id !== 'claude-code') {
    return t('quickstart.unavailableClaudeCodeOnly')
  }
  const available = keyProtocols()
  const required = selectedVariant && client.id === 'qwen-code'
    ? [selectedProtocol.value]
    : client.protocols
  return required.some((protocol) => available.includes(protocol))
    ? ''
    : t('quickstart.unavailableProtocol')
}

const clientGroups = computed<QuickstartClientGroup[]>(() => {
  const categories = [
    { id: 'coding', label: t('quickstart.categories.coding') },
    { id: 'apps', label: t('quickstart.categories.apps') },
    { id: 'build', label: t('quickstart.categories.build') },
  ] as const
  return categories.map((category) => ({
    ...category,
    clients: TK_QUICKSTART_CLIENTS
      .filter((client) => client.category === category.id)
      .sort((a, b) => a.sortOrder - b.sortOrder)
      .map((client) => {
        const reason = disabledReasonFor(client)
        return {
          id: client.id,
          name: client.name,
          icon: client.icon,
          supportTier: client.supportTier,
          disabled: Boolean(reason),
          disabledReason: reason || undefined,
        }
      }),
  }))
})

const selectedClientDisabledReason = computed(() =>
  selectedClient.value ? disabledReasonFor(selectedClient.value, true) : '',
)

function selectClient(id: string): void {
  selectedClientId.value = id
}

function openSelectedClient(): void {
  const client = selectedClient.value
  const key = selectedKey.value
  if (!client?.template || !key) return
  const url = resolveTkClientIntegrationUrl({
    template: client.template,
    apiKey: key.key,
    baseUrl: baseUrl.value,
    model: selectedModel.value,
  })
  const target = window.open(url, '_blank', 'noopener,noreferrer')
  if (target) target.opener = null
}

function parseKeyIdFromQuery(): number | null {
  const raw = route.query.keyId
  const value = Array.isArray(raw) ? raw[0] : raw
  if (!value) return null
  const id = Number.parseInt(String(value), 10)
  return Number.isFinite(id) ? id : null
}

function parseModelFromQuery(): string | null {
  const raw = route.query.model
  const value = Array.isArray(raw) ? raw[0] : raw
  if (!value) return null
  const model = String(value).trim()
  return model || null
}

function parseStringQuery(name: string): string | null {
  const raw = route.query[name]
  const value = Array.isArray(raw) ? raw[0] : raw
  const parsed = value == null ? '' : String(value).trim()
  return parsed || null
}

const initialModelFromQuery = computed(() => parseModelFromQuery())

function pickDefaultKeyId(items: ApiKey[]): number | null {
  if (!items.length) return null
  const fromQuery = parseKeyIdFromQuery()
  if (fromQuery != null && items.some((k) => k.id === fromQuery)) return fromQuery
  if (parseModelFromQuery()) {
    const universal = items.find(isUniversalKey)
    if (universal) return universal.id
  }
  const trial = items.find((k) => k.name?.toLowerCase() === 'trial')
  return (trial || items[0])?.id ?? null
}

function pickDefaultClientId(): string {
  const requested = parseStringQuery('client')
  if (requested && TK_QUICKSTART_CLIENTS.some((client) => client.id === requested)) return requested

  const model = parseModelFromQuery()
  if (model) {
    const flavor = flavorOfModel(model)
    if (flavor === 'anthropic') return 'claude-code'
    if (flavor === 'gemini') return 'gemini-cli'
    return 'codex-cli'
  }

  const platform = selectedKey.value?.group?.platform
  if (platform === PLATFORM_GEMINI || platform === PLATFORM_ANTIGRAVITY) return 'gemini-cli'
  if (platform === PLATFORM_OPENAI || platform === PLATFORM_NEWAPI || platform === PLATFORM_GROK) return 'codex-cli'
  return 'claude-code'
}

function codexWebSocketAvailable(): boolean {
  return selectedKey.value?.routing_mode === 'universal' || selectedKey.value?.group?.platform === PLATFORM_OPENAI
}

watch(selectedKey, () => {
  const protocols = keyProtocols()
  if (!protocols.includes(selectedProtocol.value)) {
    selectedProtocol.value = protocols.includes('anthropic') ? 'anthropic' : 'openai'
  }
  if (!codexWebSocketAvailable()) selectedTransport.value = 'http'
})

watch([selectedKeyId, selectedClientId, selectedProtocol, selectedTransport, selectedModel], ([keyId, clientId]) => {
  if (keyId == null || !clientId) return
  const query: Record<string, string | null | (string | null)[]> = {
    ...route.query,
    keyId: String(keyId),
    client: clientId,
  }
  if (clientId === 'qwen-code') query.protocol = selectedProtocol.value
  else delete query.protocol
  if (clientId === 'codex-cli') query.transport = selectedTransport.value
  else delete query.transport
  if (selectedModel.value) query.model = selectedModel.value
  else delete query.model
  const unchanged = Object.entries(query).every(([key, value]) => route.query[key] === value)
    && Object.keys(route.query).every((key) => key in query)
  if (!unchanged) void router.replace({ query })
})

async function loadKeys() {
  keysLoading.value = true
  keysError.value = ''
  try {
    const result = await keysAPI.list(1, 100)
    keys.value = result.items ?? []
    if (!keys.value.length) {
      const created = await keysAPI.create(
        'Quick Start',
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        undefined,
        'universal',
      )
      keys.value = [created]
    }
    const fromQuery = parseKeyIdFromQuery()
    const match = fromQuery != null ? keys.value.find((k) => k.id === fromQuery) : undefined
    selectedKeyId.value = match?.id ?? pickDefaultKeyId(keys.value)
    selectedClientId.value = pickDefaultClientId()
    selectedProtocol.value = parseStringQuery('protocol') === 'openai' ? 'openai' : 'anthropic'
    selectedTransport.value = parseStringQuery('transport') === 'websocket' ? 'websocket' : 'http'
    selectedModel.value = parseModelFromQuery() ?? ''
    const protocols = keyProtocols()
    if (!protocols.includes(selectedProtocol.value)) {
      selectedProtocol.value = protocols.includes('anthropic') ? 'anthropic' : 'openai'
    }
    if (!codexWebSocketAvailable()) selectedTransport.value = 'http'
  } catch (e: unknown) {
    keysError.value = e instanceof Error ? e.message : String(e)
  } finally {
    keysLoading.value = false
  }
}

onMounted(() => {
  loadKeys()
})
</script>
