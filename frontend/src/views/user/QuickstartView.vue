<template>
    <div class="mx-auto max-w-6xl space-y-6">
      <section>
        <div v-if="keysLoading" class="flex items-center justify-center py-6">
          <LoadingSpinner />
        </div>
        <div v-else-if="keysError" class="text-sm text-red-500">{{ keysError }}</div>
        <div v-else-if="!keys.length" class="space-y-4 text-center">
          <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('quickstart.noKeys') }}</p>
          <router-link to="/keys" class="btn btn-primary text-sm">{{ t('quickstart.createKey') }}</router-link>
        </div>
        <div v-else class="space-y-6">
          <QuickstartClientPicker
            :heading="t('quickstart.chooseClient')"
            :groups="clientGroups"
            :selected-id="selectedClientId"
            @select="selectClient"
          />

          <div
            v-if="selectedClient"
            data-tk="quickstart-config-workspace"
            class="space-y-6 border-t border-gray-200 pt-6 dark:border-dark-700"
          >
            <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                    {{ selectedClient.name }}
                  </h2>
                  <span
                    v-if="selectedClientSupportBadge"
                    class="inline-flex items-center rounded-md px-2 py-0.5 text-xs font-semibold tracking-wide"
                    :class="[
                      selectedClientSupportBadge.badgeClass,
                      selectedClientSupportBadge.tier === 'verified' ? 'uppercase' : '',
                    ]"
                  >
                    {{ selectedClientSupportBadge.label }}
                  </span>
                </div>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                  {{ selectedClientDescription }}
                </p>
              </div>
              <div class="flex shrink-0 flex-wrap gap-2">
                <button
                  v-if="showCcSwitchImport"
                  type="button"
                  data-tk="quickstart-ccs-import"
                  class="btn btn-primary inline-flex items-center gap-1.5 text-sm"
                  :disabled="Boolean(selectedClientDisabledReason)"
                  @click="importSelectedClientToCcSwitch"
                >
                  <Icon name="download" size="sm" />
                  {{ t('quickstart.importToCcSwitch') }}
                </button>
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

            <details
              ref="advancedOptionsRef"
              data-tk="quickstart-advanced-options"
              class="rounded-lg border border-gray-200 bg-gray-50/70 dark:border-dark-600 dark:bg-dark-800/40"
              :open="advancedOptionsOpen"
            >
              <summary
                class="cursor-pointer select-none px-4 py-3 text-sm font-medium text-gray-700 dark:text-gray-200"
                @click.prevent="advancedOptionsOpen = !advancedOptionsOpen"
              >
                {{ t('quickstart.advancedOptions') }}
              </summary>
              <div class="space-y-4 border-t border-gray-200 px-4 py-4 dark:border-dark-600">
                <p v-if="!keyManuallySelected && selectedKey" class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('quickstart.keyAutoSelected') }}
                </p>
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
                      @change="onKeyManuallySelected"
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
              </div>
            </details>

            <QuickstartConnectionHealth
              v-if="selectedClientDisabledReason"
              layout="banner"
              :test-state="connectionTestState"
              setup-blocked
              :setup-blocked-reason="selectedClientDisabledReason || undefined"
              @change-key="openAdvancedKeyOptions"
            />

            <template v-if="selectedKey && !selectedClientDisabledReason">
              <div
                data-tk="quickstart-connection-row"
                class="flex w-full flex-wrap items-center gap-x-3 gap-y-2"
              >
                <div v-if="selectedClient.id === 'qwen-code'" class="flex flex-wrap items-center gap-3">
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
                      @click="!protocol.disabled && onProtocolSelected(protocol.id)"
                    >
                      {{ protocol.label }}
                    </button>
                  </div>
                </div>

                <div v-if="selectedClient.id === 'codex-cli'" class="flex flex-wrap items-center gap-3">
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
                      @click="!transport.disabled && onTransportSelected(transport.id)"
                    >
                      {{ transport.label }}
                    </button>
                  </div>
                </div>

                <QuickstartConnectionHealth
                  layout="inline"
                  :test-state="connectionTestState"
                  :setup-blocked="!selectedModel"
                  @run-test="runConnectionTest"
                  @change-key="openAdvancedKeyOptions"
                />
              </div>

              <UseKeyGuide
                ref="useKeyGuideRef"
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
                hide-inline-test
                @model-change="selectedModel = $event"
                @test-state-change="connectionTestState = $event"
              />
            </template>
          </div>
        </div>
      </section>

      <div class="flex flex-wrap items-center justify-center gap-4 pb-6">
        <router-link to="/keys" class="btn btn-secondary text-sm">{{ t('quickstart.manageKeys') }}</router-link>
        <router-link to="/models?view=pricing" class="btn btn-secondary text-sm">{{ t('quickstart.viewPricing') }}</router-link>
        <router-link to="/studio" class="btn btn-primary text-sm">{{ t('quickstart.tryStudio') }}</router-link>
      </div>
    </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import * as keysAPI from '@/api/keys'
import type { ApiKey } from '@/types'
import { filterUserSelectableApiKeys } from '@/utils/reservedProbeKey.tk'
import { isUniversalKey } from '@/utils/studioUniversalKey.tk'
import {
  keyProtocolsForApiKey,
  quickstartKeyDisabledReason,
  recommendKeyForClient,
} from '@/utils/quickstartKeyMatch.tk'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import QuickstartClientPicker, { type QuickstartClientGroup } from '@/components/keys/QuickstartClientPicker.vue'
import QuickstartConnectionHealth from '@/components/keys/QuickstartConnectionHealth.vue'
import UseKeyGuide from '@/components/keys/UseKeyGuide.vue'
import type { TestState } from '@/composables/useTkUseKey'
import {
  resolveTkClientIntegrationUrl,
  TK_QUICKSTART_CLIENTS,
  TK_CLIENT_SUPPORT_META,
  type TkClientCatalogEntry,
} from '@/constants/clientIntegrations.tk'
import { flavorOfModel } from '@/composables/useTkUseKey'
import { gatewayWarmupConnection } from '@/api/playground'
import { PLATFORM_OPENAI } from '@/constants/gatewayPlatforms'
import { useCcSwitchImport } from '@/composables/useCcSwitchImport'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const { importToCcSwitch } = useCcSwitchImport()

const keys = ref<ApiKey[]>([])
const keysLoading = ref(true)
const keysError = ref('')
const selectedKeyId = ref<number | null>(null)
const selectedClientId = ref('')
const selectedProtocol = ref<'anthropic' | 'openai'>('anthropic')
const selectedTransport = ref<'http' | 'websocket'>('http')
const selectedModel = ref('')
const keyManuallySelected = ref(false)
const advancedOptionsOpen = ref(false)
const advancedOptionsRef = ref<HTMLDetailsElement | null>(null)
const useKeyGuideRef = ref<InstanceType<typeof UseKeyGuide> | null>(null)
const connectionTestState = ref<TestState>({ status: 'idle' })

const selectedOptionClass = 'bg-primary-600 text-white shadow-sm'
const idleOptionClass = 'text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700'

const keyMatchOptions = computed(() => ({
  protocol: selectedProtocol.value,
  transport: selectedTransport.value,
}))

const qwenProtocols = computed(() => {
  const key = selectedKey.value
  const available = key ? keyProtocolsForApiKey(key) : []
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

const selectedClientSupportBadge = computed(() => {
  const client = selectedClient.value
  if (!client) return null
  const meta = TK_CLIENT_SUPPORT_META[client.supportTier]
  return {
    tier: client.supportTier,
    label: t(meta.labelKey),
    badgeClass: meta.badgeClass,
  }
})

const showCcSwitchImport = computed(() => {
  const client = selectedClient.value
  if (!client?.ccsApp) return false
  if (appStore.cachedPublicSettings?.hide_ccs_import_button) return false
  return client.action === 'ccs-import'
})

const selectedClientDisabledReason = computed(() => {
  const client = selectedClient.value
  if (!client) return ''
  return disabledReasonFor(client, true)
})

function maskKey(key: string) {
  if (key.length <= 14) return key
  return `${key.slice(0, 6)}${'•'.repeat(8)}${key.slice(-4)}`
}

function clientListDisabledReason(client: TkClientCatalogEntry): string {
  if (!keys.value.length) return t('quickstart.unavailableNoGroup')
  const compatible = keys.value.some((key) => !quickstartKeyDisabledReason(key, client, {}, t))
  if (compatible) return ''
  const sample = recommendKeyForClient(keys.value, client) ?? keys.value[0]
  return quickstartKeyDisabledReason(sample, client, {}, t)
}

function disabledReasonFor(client: TkClientCatalogEntry, selectedVariant = false): string {
  const key = selectedKey.value
  if (!key) return t('quickstart.unavailableNoGroup')
  const options = selectedVariant && client.id === 'qwen-code'
    ? { protocol: selectedProtocol.value, transport: selectedTransport.value }
    : keyMatchOptions.value
  return quickstartKeyDisabledReason(key, client, options, t)
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
        const reason = clientListDisabledReason(client)
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

function applyRecommendedKey(client = selectedClient.value, preserveManual = keyManuallySelected.value): void {
  if (!client || preserveManual) return
  const recommended = recommendKeyForClient(keys.value, client, keyMatchOptions.value)
  if (recommended) selectedKeyId.value = recommended.id
}

function selectClient(id: string): void {
  selectedClientId.value = id
  keyManuallySelected.value = false
  connectionTestState.value = { status: 'idle' }
  applyRecommendedKey(TK_QUICKSTART_CLIENTS.find((client) => client.id === id) ?? null)
}

function onKeyManuallySelected(): void {
  keyManuallySelected.value = true
}

function onProtocolSelected(protocol: 'anthropic' | 'openai'): void {
  selectedProtocol.value = protocol
  if (!keyManuallySelected.value) applyRecommendedKey()
}

function onTransportSelected(transport: 'http' | 'websocket'): void {
  selectedTransport.value = transport
  if (!keyManuallySelected.value) applyRecommendedKey()
}

function openAdvancedKeyOptions(): void {
  advancedOptionsOpen.value = true
  advancedOptionsRef.value?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
}

function runConnectionTest(): void {
  useKeyGuideRef.value?.runTest()
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

function importSelectedClientToCcSwitch(): void {
  const client = selectedClient.value
  const key = selectedKey.value
  if (!client?.ccsApp || !key) return
  const providerName = (appStore.cachedPublicSettings?.site_name || 'TokenKey').trim() || 'TokenKey'
  importToCcSwitch({
    key,
    ccsApp: client.ccsApp,
    baseUrl: baseUrl.value,
    providerName,
  })
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

  return 'claude-code'
}

function pickDefaultKeyId(client: TkClientCatalogEntry, items: ApiKey[]): number | null {
  if (!items.length) return null
  const fromQuery = parseKeyIdFromQuery()
  if (fromQuery != null && items.some((k) => k.id === fromQuery)) {
    keyManuallySelected.value = true
    return fromQuery
  }
  const recommended = recommendKeyForClient(items, client, keyMatchOptions.value)
  if (recommended) return recommended.id
  if (parseModelFromQuery()) {
    const universal = items.find(isUniversalKey)
    if (universal) return universal.id
  }
  const trial = items.find((k) => k.name?.toLowerCase() === 'trial')
  return (trial || items[0])?.id ?? null
}

function codexWebSocketAvailable(): boolean {
  return selectedKey.value?.routing_mode === 'universal' || selectedKey.value?.group?.platform === PLATFORM_OPENAI
}

watch(selectedKey, () => {
  const key = selectedKey.value
  if (!key) return
  const protocols = keyProtocolsForApiKey(key)
  if (!protocols.includes(selectedProtocol.value)) {
    selectedProtocol.value = protocols.includes('anthropic') ? 'anthropic' : 'openai'
  }
  if (!codexWebSocketAvailable()) selectedTransport.value = 'http'
})

watch([selectedKey, baseUrl], ([key, url]) => {
  if (!key?.key || !url) return
  if (isUniversalKey(key)) return
  void gatewayWarmupConnection(key.key, url)
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
    const result = await keysAPI.list(1, 100, { status: 'active' })
    keys.value = filterUserSelectableApiKeys(result.items ?? [])
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

    selectedClientId.value = pickDefaultClientId()
    selectedProtocol.value = parseStringQuery('protocol') === 'openai' ? 'openai' : 'anthropic'
    selectedTransport.value = parseStringQuery('transport') === 'websocket' ? 'websocket' : 'http'
    selectedModel.value = parseModelFromQuery() ?? ''

    const client = TK_QUICKSTART_CLIENTS.find((entry) => entry.id === selectedClientId.value)
    if (client) {
      selectedKeyId.value = pickDefaultKeyId(client, keys.value)
    }

    const key = selectedKey.value
    if (key) {
      const protocols = keyProtocolsForApiKey(key)
      if (!protocols.includes(selectedProtocol.value)) {
        selectedProtocol.value = protocols.includes('anthropic') ? 'anthropic' : 'openai'
      }
      if (!codexWebSocketAvailable()) selectedTransport.value = 'http'
    }
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
