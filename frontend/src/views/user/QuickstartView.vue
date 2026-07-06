<template>
  <AppLayout>
    <div class="mx-auto max-w-4xl space-y-6 py-4">
      <div class="text-center">
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white sm:text-3xl">
          {{ t('quickstart.title') }}
        </h1>
        <p class="mt-2 text-gray-500 dark:text-gray-400">
          {{ t('quickstart.subtitle') }}
        </p>
      </div>

      <section class="rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-700 dark:bg-gray-800">
        <div v-if="keysLoading" class="flex items-center justify-center py-6">
          <LoadingSpinner />
        </div>
        <div v-else-if="keysError" class="text-sm text-red-500">{{ keysError }}</div>
        <div v-else-if="!keys.length" class="space-y-4 text-center">
          <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('quickstart.noKeys') }}</p>
          <router-link to="/keys" class="btn btn-primary text-sm">{{ t('quickstart.createKey') }}</router-link>
        </div>
        <div v-else class="space-y-4">
          <div class="flex flex-col gap-3 sm:flex-row sm:items-end">
            <div class="flex-1">
              <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t('quickstart.selectKey') }}
              </label>
              <select
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

          <UseKeyGuide
            v-if="selectedKey"
            :api-key="selectedKey.key"
            :api-key-id="selectedKey.id"
            :base-url="baseUrl"
            :platform="selectedKey.group?.platform ?? null"
            :routing-mode="selectedKey.routing_mode"
            :initial-model="initialModelFromQuery"
            :claude-code-only="selectedKey.group?.claude_code_only || false"
            :allow-messages-dispatch="selectedKey.group?.allow_messages_dispatch || false"
            :supported-model-scopes="selectedKey.group?.supported_model_scopes"
          />
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
import UseKeyGuide from '@/components/keys/UseKeyGuide.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()

const keys = ref<ApiKey[]>([])
const keysLoading = ref(true)
const keysError = ref('')
const selectedKeyId = ref<number | null>(null)

const baseUrl = computed(() => {
  const raw = appStore.cachedPublicSettings?.api_base_url || window.location.origin
  return raw.replace(/\/+$/, '')
})

const selectedKey = computed(() =>
  keys.value.find((k) => k.id === selectedKeyId.value) ?? null,
)

function maskKey(key: string) {
  if (key.length <= 14) return key
  return `${key.slice(0, 6)}${'•'.repeat(8)}${key.slice(-4)}`
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

watch(selectedKeyId, (id) => {
  if (id == null) return
  const current = parseKeyIdFromQuery()
  if (current === id) return
  router.replace({ query: { ...route.query, keyId: String(id) } })
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
