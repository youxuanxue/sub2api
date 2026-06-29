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
            :aria-selected="view === 'chat'"
            class="rounded-lg px-4 py-1.5 transition-colors"
            :class="view === 'chat' ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500' : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'"
            data-testid="studio-mode-chat"
            @click="view = 'chat'"
          >
            {{ t('studio.modeChat') }}
          </button>
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

      <div
        v-if="!loadError && !probed"
        class="flex min-h-[420px] items-center justify-center rounded-xl border border-dashed border-gray-200 bg-white text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-dark-400"
        data-testid="studio-bootstrap-loading"
      >
        {{ t('studio.loadingModels') }}
      </div>

      <ChatStudio
        v-if="!loadError && probed && view === 'chat'"
        :api-key="apiKey"
        :gateway-base="gatewayBase"
        :available-ids="availableIds"
      />
      <ImageStudio
        v-else-if="userReady && !loadError && probed && view === 'image'"
        :api-key="apiKey"
        :gateway-base="gatewayBase"
        :available-ids="availableIds"
        :price-map="priceMap"
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
        :price-map="priceMap"
        :balance="balance"
        :user-id="userId"
        :key-id="selectedKeyId"
        :keys="keys"
        :rate-multiplier="1"
        :any-key-serves-video="anyKeyServesVideo"
        @spent="refreshBalance"
      />
      <BakeOff
        v-else-if="userReady && !loadError && probed && view === 'bakeoff'"
        :api-key="apiKey"
        :gateway-base="gatewayBase"
        :available-ids="availableIds"
        :price-map="priceMap"
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
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import ChatStudio from '@/views/user/studio/ChatStudio.vue'
import ImageStudio from '@/views/user/studio/ImageStudio.vue'
import VideoStudio from '@/views/user/studio/VideoStudio.vue'
import BakeOff from '@/views/user/studio/BakeOff.vue'
import { keysAPI } from '@/api/keys'
import { gatewayListModels, resolveGatewayBaseUrl } from '@/api/playground'
import { getMePricingCatalog } from '@/api/me-pricing'
import { getPublicPricing } from '@/api/pricing'
import { formatUsd } from '@/utils/mediaCostEstimate.tk'
import {
  entitledModelIds,
  isUniversalKey,
  priceMapFromPublicCatalog,
} from '@/utils/studioUniversalKey.tk'
import {
  groupServes,
  pickModalityKey,
  type ModalityKeyOption,
  type PickerModality,
  type MediaPrice,
  type MediaPriceMap,
} from '@/constants/mediaTiers.tk'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/types'

const { t } = useI18n()
const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()

// Chat is the default landing modality: zero-cost + almost every group serves a
// chat model, so the first touch "just works" (image/video need a Vertex/Volc
// group and can dead-end). `?mode=chat|image|video|bakeoff` (compare = bakeoff)
// deep-links a tab — e.g. the retired /playground redirects here with mode=chat.
const VIEW_MODES = ['chat', 'image', 'video', 'bakeoff'] as const
type StudioView = (typeof VIEW_MODES)[number]
function initialView(): StudioView {
  const m = String(route.query.mode || '').toLowerCase()
  if (m === 'compare') return 'bakeoff'
  return (VIEW_MODES as readonly string[]).includes(m) ? (m as StudioView) : 'chat'
}
const view = ref<StudioView>(initialView())
const keys = ref<ApiKey[]>([])
const selectedKeyId = ref<number | null>(null)
const gatewayBase = ref('')
// Per-group model pools, keyed by groupKeyOf(). Probed once for every distinct
// group up front so the picker (and the dropdown annotations) can reason about
// EVERY key's modality, not just the one currently selected.
const groupModelSets = ref<Map<string, Set<string>>>(new Map())
/** Cross-group entitlement index (me/pricing-catalog authorized_groups_by_model). */
const userEntitledIds = ref<Set<string>>(new Set())
const universalPriceMap = ref<MediaPriceMap>(new Map())
// Live per-model price for the SELECTED key's group (getMePricingCatalog) — the
// single source of media prices (no hardcoding, so prices can't drift).
const priceMap = ref<Map<string, MediaPrice>>(new Map())
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
  if (isUniversalKey(k)) return userEntitledIds.value
  return groupModelSets.value.get(groupKeyOf(k)) ?? new Set<string>()
}

function keyServesModality(k: ApiKey, modality: PickerModality): boolean {
  return groupServes(modality, availableIdsOf(k))
}

// Model pool of the currently selected key — what the child studios resolve
// tiers against.
const availableIds = computed<Set<string>>(() =>
  selectedKey.value ? availableIdsOf(selectedKey.value) : new Set<string>()
)

const anyKeyServesVideo = computed(() => keys.value.some((k) => keyServesModality(k, 'video')))

// The single modality the picker can optimize a key for. Bake-off has its OWN
// internal image/video toggle, so no single key serves both of its sub-modes —
// we leave its key selection to the user there (null = don't auto-pick / annotate).
// Chat / image / video each map straight to a PickerModality.
const pickerModality = computed<PickerModality | null>(() =>
  view.value === 'bakeoff' ? null : view.value
)

function modalityOptions(): ModalityKeyOption[] {
  return keys.value.map((k) => ({
    id: k.id,
    isTrial: k.name?.toLowerCase() === 'trial',
    availableIds: availableIdsOf(k),
  }))
}

function keyLabel(k: ApiKey): string {
  const group = isUniversalKey(k) ? t('studio.universalKeyBadge') : k.group?.name || t('studio.defaultGroup')
  const base = `${k.name || k.id} · ${group}`
  const m = pickerModality.value
  if (probed.value && m && !keyServesModality(k, m)) {
    return `${base} · ${t('studio.keyNoModality')}`
  }
  return base
}

async function loadUserEntitlement(): Promise<void> {
  try {
    const [meCatalog, publicCatalog] = await Promise.all([getMePricingCatalog(), getPublicPricing()])
    const entitled = entitledModelIds(meCatalog)
    userEntitledIds.value = entitled
    universalPriceMap.value = priceMapFromPublicCatalog(publicCatalog.data || [], entitled)
  } catch {
    userEntitledIds.value = new Set()
    universalPriceMap.value = new Map()
  }
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

// Live media prices for the selected key's group. your_price from the catalog is
// the OFFICIAL list price (rate-decoupled by ops policy) — exactly what the cost
// mirror needs (settlement applies the rate later), so the studio keeps
// rateMultiplier=1. A model with no per_image/per_second price is simply omitted
// (priced ∩ servable). Failure → empty map (models hide) rather than stale prices.
async function loadPriceMap(keyId: number): Promise<void> {
  const k = keys.value.find((x) => x.id === keyId)
  if (k && isUniversalKey(k)) {
    priceMap.value = new Map(universalPriceMap.value)
    return
  }
  try {
    const catalog = await getMePricingCatalog({ apiKeyId: keyId })
    const next = new Map<string, MediaPrice>()
    for (const m of catalog.models || []) {
      const perImage = m.your_price?.per_image
      const perSecond = m.your_price?.per_second
      if (perImage != null || perSecond != null) {
        next.set(m.model_id, { perImage: perImage ?? undefined, perSecond: perSecond ?? undefined })
      }
    }
    priceMap.value = next
  } catch {
    priceMap.value = new Map()
  }
}

// Re-pick when the modality tab changes: if the current key already serves the
// new modality it is kept, otherwise we move to one that does (the dropdown
// still lets the user override). Bake-off (null modality) keeps the current key.
watch(view, () => {
  if (!probed.value) return
  const m = pickerModality.value
  if (!m) return
  selectedKeyId.value = pickModalityKey(modalityOptions(), m, selectedKeyId.value)
})

// Refetch the price catalog whenever the selected key changes (prices are
// per-group). Bootstrap awaits the first load before mounting the studios.
watch(selectedKeyId, (id) => {
  if (probed.value && id != null) void loadPriceMap(id)
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
    await Promise.all([probeAllGroups(), loadUserEntitlement()])
    if (loadError.value) return
    // Seed with the historical default, then let the picker move to a key whose
    // group actually serves the landing modality (defaults to chat at mount).
    selectedKeyId.value = pickModalityKey(modalityOptions(), pickerModality.value ?? 'chat', seed)
    // Mount studios as soon as /v1/models is probed. Price catalog is only needed
    // for image/video/bake-off; awaiting it blocked Chat on slow catalog fetches.
    probed.value = true
    if (selectedKeyId.value != null) void loadPriceMap(selectedKeyId.value)
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : t('studio.loadFailed')
  }
}

onMounted(() => {
  void bootstrap()
})
</script>
