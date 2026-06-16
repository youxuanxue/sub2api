<template>
  <AppLayout>
    <div class="mx-auto flex max-w-6xl flex-col gap-5 px-4 py-6 lg:px-6">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div class="space-y-1">
          <h1 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white">{{ t('studio.title') }}</h1>
          <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('studio.subtitle') }}</p>
        </div>
        <div class="flex items-center gap-2 text-sm">
          <span class="text-gray-500 dark:text-dark-400">{{ t('studio.balance') }}</span>
          <span class="rounded-lg bg-primary-50 px-3 py-1.5 font-semibold text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">
            {{ formatUsd(balance) }}
          </span>
        </div>
      </header>

      <!-- No key: gate to /keys (login itself is enforced by the router guard). -->
      <div
        v-if="loadError"
        class="rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/40 dark:text-amber-100"
      >
        {{ loadError }}
        <router-link class="ml-2 font-medium text-primary-600 underline dark:text-primary-400" to="/keys">
          {{ t('studio.manageKeys') }}
        </router-link>
      </div>

      <div
        v-else
        class="flex flex-wrap items-center gap-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900"
      >
        <!-- Modality first: the explicit, user-owned decision (replaces id-prefix guessing). -->
        <div role="tablist" class="flex rounded-xl border border-gray-200 bg-gray-50 p-1 text-sm font-medium dark:border-dark-700 dark:bg-dark-800">
          <button
            role="tab"
            type="button"
            :aria-selected="view === 'image'"
            class="rounded-lg px-4 py-1.5 transition-colors"
            :class="view === 'image' ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500' : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'"
            data-testid="studio-mode-image"
            @click="view = 'image'"
          >
            {{ t('studio.modeImage') }}
          </button>
          <button
            role="tab"
            type="button"
            :aria-selected="view === 'video'"
            class="rounded-lg px-4 py-1.5 transition-colors"
            :class="view === 'video' ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500' : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'"
            data-testid="studio-mode-video"
            @click="view = 'video'"
          >
            {{ t('studio.modeVideo') }}
          </button>
          <button
            role="tab"
            type="button"
            :aria-selected="view === 'bakeoff'"
            class="rounded-lg px-4 py-1.5 transition-colors"
            :class="view === 'bakeoff' ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500' : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'"
            data-testid="studio-mode-bakeoff"
            @click="view = 'bakeoff'"
          >
            {{ t('studio.modeBakeoff') }}
          </button>
        </div>

        <div class="min-w-[220px] flex-1">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400" for="studio-key">{{
            t('studio.apiKey')
          }}</label>
          <select
            id="studio-key"
            v-model="selectedKeyId"
            class="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
            :disabled="!keys.length"
          >
            <option v-if="!keys.length" disabled :value="null">{{ t('studio.pickKeyPlaceholder') }}</option>
            <option v-for="k in keys" :key="k.id" :value="k.id">{{ keyLabel(k) }}</option>
          </select>
        </div>

        <p v-if="modelsLoading" class="text-xs text-gray-400 dark:text-dark-500">{{ t('studio.loadingModels') }}</p>
      </div>

      <ImageStudio
        v-if="userReady && !loadError && probed && view === 'image'"
        :api-key="apiKey"
        :gateway-base="gatewayBase"
        :available-ids="availableIds"
        :balance="balance"
        :user-id="userId"
        :rate-multiplier="1"
        @spent="refreshBalance"
      />
      <VideoStudio
        v-else-if="userReady && !loadError && probed && view === 'video'"
        :api-key="apiKey"
        :gateway-base="gatewayBase"
        :available-ids="availableIds"
        :balance="balance"
        :user-id="userId"
        :key-id="selectedKeyId"
        :keys="keys"
        :rate-multiplier="1"
        @spent="refreshBalance"
      />
      <BakeOff
        v-else-if="userReady && !loadError && probed && view === 'bakeoff'"
        :api-key="apiKey"
        :gateway-base="gatewayBase"
        :available-ids="availableIds"
        :balance="balance"
        :key-id="selectedKeyId"
        :keys="keys"
        :rate-multiplier="1"
        @spent="refreshBalance"
      />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import ImageStudio from '@/views/user/studio/ImageStudio.vue'
import VideoStudio from '@/views/user/studio/VideoStudio.vue'
import BakeOff from '@/views/user/studio/BakeOff.vue'
import { keysAPI } from '@/api/keys'
import { gatewayListModels, resolveGatewayBaseUrl } from '@/api/playground'
import { formatUsd } from '@/utils/mediaCostEstimate.tk'
import {
  modalityHasTiers,
  pickModalityKey,
  type ModalityKeyOption,
  type StudioModality,
} from '@/constants/mediaTiers.tk'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const view = ref<'image' | 'video' | 'bakeoff'>('image')
const keys = ref<ApiKey[]>([])
const selectedKeyId = ref<number | null>(null)
const gatewayBase = ref('')
// Per-group model pools, keyed by groupKeyOf(). Probed once for every distinct
// group up front so the picker (and the dropdown annotations) can reason about
// EVERY key's modality, not just the one currently selected.
const groupModelSets = ref<Map<string, Set<string>>>(new Map())
const modelsLoading = ref(false)
const probed = ref(false)
const loadError = ref('')

const selectedKey = computed(() => keys.value.find((k) => k.id === selectedKeyId.value))
const apiKey = computed(() => selectedKey.value?.key || '')
const balance = computed(() => authStore.user?.balance ?? 0)
// Only mount the studios once a real user id is available, so the media library
// is never keyed to a throwaway "anon" bucket during a brief auth-null window
// (would split history across two localStorage keys).
const userReady = computed(() => authStore.user?.id != null)
const userId = computed(() => authStore.user?.id ?? 'anon')

// Keys in the same group share one /v1/models pool — dedup the probe by group,
// falling back to the key id for the (rare) ungrouped key.
function groupKeyOf(k: ApiKey): string {
  return k.group?.id != null ? `g${k.group.id}` : `k${k.id}`
}
function availableIdsOf(k: ApiKey): Set<string> {
  return groupModelSets.value.get(groupKeyOf(k)) ?? new Set<string>()
}

// Model pool of the currently selected key — what the child studios resolve
// tiers against.
const availableIds = computed<Set<string>>(() =>
  selectedKey.value ? availableIdsOf(selectedKey.value) : new Set<string>()
)

// Modality the picker cares about: bake-off compares image models, so it shares
// the image pool requirement.
const pickerModality = computed<StudioModality>(() => (view.value === 'video' ? 'video' : 'image'))

function modalityOptions(): ModalityKeyOption[] {
  return keys.value.map((k) => ({
    id: k.id,
    isTrial: k.name?.toLowerCase() === 'trial',
    availableIds: availableIdsOf(k),
  }))
}

function keyLabel(k: ApiKey): string {
  const group = k.group?.name || t('studio.defaultGroup')
  const base = `${k.name || k.id} · ${group}`
  // Once probed, flag keys whose group can't serve the active modality so the
  // user isn't left guessing which key to pick.
  if (probed.value && !modalityHasTiers(pickerModality.value, availableIdsOf(k))) {
    return `${base} · ${t('studio.keyNoModality')}`
  }
  return base
}

async function refreshBalance(): Promise<void> {
  try {
    await authStore.refreshUser()
  } catch {
    /* balance refresh is best-effort; the 60s auto-refresh will catch up */
  }
}

// Probe /v1/models for every distinct group, in parallel. A failed probe maps to
// an empty pool (so that group's tiers stay hidden); only an all-failed result
// is surfaced as a hard load error.
async function probeAllGroups(): Promise<void> {
  const reps = new Map<string, ApiKey>()
  for (const k of keys.value) {
    const gk = groupKeyOf(k)
    if (!reps.has(gk)) reps.set(gk, k)
  }
  const entries = [...reps.entries()]
  if (entries.length === 0) return
  modelsLoading.value = true
  try {
    const results = await Promise.allSettled(
      entries.map(([, k]) => gatewayListModels(k.key, gatewayBase.value))
    )
    const next = new Map<string, Set<string>>()
    let anyOk = false
    results.forEach((r, i) => {
      const gk = entries[i][0]
      if (r.status === 'fulfilled') {
        anyOk = true
        next.set(gk, new Set((r.value.data || []).map((m) => m.id)))
      } else {
        next.set(gk, new Set())
      }
    })
    groupModelSets.value = next
    if (!anyOk) loadError.value = t('studio.loadFailed')
  } finally {
    modelsLoading.value = false
  }
}

// Re-pick when the modality tab changes: if the current key already serves the
// new modality it is kept, otherwise we move to one that does (the dropdown
// still lets the user override).
watch(view, () => {
  if (!probed.value) return
  selectedKeyId.value = pickModalityKey(modalityOptions(), pickerModality.value, selectedKeyId.value)
})

async function bootstrap(): Promise<void> {
  loadError.value = ''
  await appStore.fetchPublicSettings()
  gatewayBase.value = resolveGatewayBaseUrl(appStore.apiBaseUrl || appStore.cachedPublicSettings?.api_base_url)
  try {
    const page = await keysAPI.list(1, 50, { status: 'active' })
    keys.value = (page.items || []).filter((k) => !!k.key)
    const trial = keys.value.find((k) => k.name?.toLowerCase() === 'trial')
    const seed = (trial || keys.value[0])?.id ?? null
    if (seed == null) {
      loadError.value = t('studio.noApiKey')
      return
    }
    await probeAllGroups()
    if (loadError.value) return
    // Seed with the historical default, then let the picker move to a key whose
    // group actually serves the landing modality.
    selectedKeyId.value = pickModalityKey(modalityOptions(), pickerModality.value, seed)
    probed.value = true
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : t('studio.loadFailed')
  }
}

onMounted(() => {
  void bootstrap()
})
</script>
