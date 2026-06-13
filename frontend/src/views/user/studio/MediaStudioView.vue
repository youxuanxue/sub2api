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
            :aria-selected="modality === 'image'"
            class="rounded-lg px-4 py-1.5 transition-colors"
            :class="modality === 'image' ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500' : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'"
            data-testid="studio-mode-image"
            @click="modality = 'image'"
          >
            {{ t('studio.modeImage') }}
          </button>
          <button
            role="tab"
            type="button"
            :aria-selected="modality === 'video'"
            class="rounded-lg px-4 py-1.5 transition-colors"
            :class="modality === 'video' ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500' : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'"
            data-testid="studio-mode-video"
            @click="modality = 'video'"
          >
            {{ t('studio.modeVideo') }}
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
        v-if="userReady && !loadError && modality === 'image'"
        :api-key="apiKey"
        :gateway-base="gatewayBase"
        :available-ids="availableIds"
        :balance="balance"
        :user-id="userId"
        :rate-multiplier="1"
        @spent="refreshBalance"
      />
      <VideoStudio
        v-else-if="userReady && !loadError && modality === 'video'"
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
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import ImageStudio from '@/views/user/studio/ImageStudio.vue'
import VideoStudio from '@/views/user/studio/VideoStudio.vue'
import { keysAPI } from '@/api/keys'
import { gatewayListModels, resolveGatewayBaseUrl } from '@/api/playground'
import { formatUsd } from '@/utils/mediaCostEstimate.tk'
import type { StudioModality } from '@/constants/mediaTiers.tk'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const modality = ref<StudioModality>('image')
const keys = ref<ApiKey[]>([])
const selectedKeyId = ref<number | null>(null)
const gatewayBase = ref('')
const availableIds = ref<Set<string>>(new Set())
const modelsLoading = ref(false)
const loadError = ref('')

const selectedKey = computed(() => keys.value.find((k) => k.id === selectedKeyId.value))
const apiKey = computed(() => selectedKey.value?.key || '')
const balance = computed(() => authStore.user?.balance ?? 0)
// Only mount the studios once a real user id is available, so the media library
// is never keyed to a throwaway "anon" bucket during a brief auth-null window
// (would split history across two localStorage keys).
const userReady = computed(() => authStore.user?.id != null)
const userId = computed(() => authStore.user?.id ?? 'anon')

function keyLabel(k: ApiKey): string {
  const group = k.group?.name || t('studio.defaultGroup')
  return `${k.name || k.id} · ${group}`
}

let modelsAbort: AbortController | null = null

async function loadModelsForKey(key: string): Promise<void> {
  modelsAbort?.abort()
  const ctrl = new AbortController()
  modelsAbort = ctrl
  loadError.value = ''
  availableIds.value = new Set()
  modelsLoading.value = true
  try {
    const list = await gatewayListModels(key, gatewayBase.value, ctrl.signal)
    if (ctrl.signal.aborted) return
    availableIds.value = new Set((list.data || []).map((m) => m.id))
    if (availableIds.value.size === 0) {
      loadError.value = t('studio.noModels')
    }
  } catch (e) {
    if (ctrl.signal.aborted) return
    loadError.value = e instanceof Error ? e.message : t('studio.loadFailed')
  } finally {
    if (modelsAbort === ctrl) modelsLoading.value = false
  }
}

async function refreshBalance(): Promise<void> {
  try {
    await authStore.refreshUser()
  } catch {
    /* balance refresh is best-effort; the 60s auto-refresh will catch up */
  }
}

watch(selectedKeyId, () => {
  if (apiKey.value) void loadModelsForKey(apiKey.value)
})

async function bootstrap(): Promise<void> {
  loadError.value = ''
  await appStore.fetchPublicSettings()
  gatewayBase.value = resolveGatewayBaseUrl(appStore.apiBaseUrl || appStore.cachedPublicSettings?.api_base_url)
  try {
    const page = await keysAPI.list(1, 50, { status: 'active' })
    keys.value = (page.items || []).filter((k) => !!k.key)
    const trial = keys.value.find((k) => k.name?.toLowerCase() === 'trial')
    const pick = trial || keys.value[0]
    if (!pick) {
      loadError.value = t('studio.noApiKey')
      return
    }
    selectedKeyId.value = pick.id
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : t('studio.loadFailed')
  }
}

onMounted(() => {
  void bootstrap()
})
</script>
