<template>
  <div class="space-y-4">
    <div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
      <div class="mb-3 flex flex-wrap items-center gap-3">
        <div role="tablist" class="flex rounded-lg border border-gray-200 bg-gray-50 p-0.5 text-sm font-medium dark:border-dark-700 dark:bg-dark-800">
          <button
            type="button"
            class="rounded-md px-3 py-1 transition-colors"
            :class="modality === 'image' ? 'bg-primary-600 text-white' : 'text-gray-600 dark:text-dark-300'"
            data-testid="bakeoff-mode-image"
            @click="setModality('image')"
          >
            {{ t('studio.modeImage') }}
          </button>
          <button
            type="button"
            class="rounded-md px-3 py-1 transition-colors"
            :class="modality === 'video' ? 'bg-primary-600 text-white' : 'text-gray-600 dark:text-dark-300'"
            data-testid="bakeoff-mode-video"
            @click="setModality('video')"
          >
            {{ t('studio.modeVideo') }}
          </button>
        </div>
        <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('studio.bakeoff.hint') }}</p>
      </div>

      <div v-if="models.length < 2" class="rounded-lg border border-dashed border-gray-300 bg-gray-50 p-4 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400">
        {{ t('studio.bakeoff.needTwo') }}
      </div>

      <template v-else>
        <textarea
          v-model="prompt"
          rows="2"
          class="w-full resize-none rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
          :placeholder="t('studio.bakeoff.promptPlaceholder')"
          :disabled="running"
        />

        <div class="mt-3 flex flex-wrap items-center gap-2">
          <span class="text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ t('studio.bakeoff.pickModels') }}</span>
          <button
            v-for="r in models"
            :key="r.model.modelId"
            type="button"
            class="rounded-lg border px-3 py-1.5 text-sm font-medium transition"
            :class="selectedModelIds.includes(r.model.modelId)
              ? 'border-primary-600 bg-primary-600 text-white'
              : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-dark-300'"
            :disabled="running || (!selectedModelIds.includes(r.model.modelId) && selectedModelIds.length >= MAX_PANELS)"
            data-testid="bakeoff-tier"
            @click="toggleModel(r.model.modelId)"
          >
            {{ r.model.displayName }}
            <span class="opacity-70">{{ modality === 'image' ? formatUsd(r.baseImagePrice || 0) + t('studio.image.perImageUnit') : formatUsd(r.perSecond || 0) + t('studio.video.perSecondUnit') }}</span>
          </button>
        </div>

        <div v-if="modality === 'video'" class="mt-3 flex items-center gap-3">
          <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('studio.video.duration') }}</span>
          <input v-model.number="duration" type="range" :min="VIDEO_DURATION_MIN" :max="VIDEO_DURATION_MAX" step="1" class="w-48 accent-primary-600" :disabled="running" />
          <span class="rounded-md bg-primary-50 px-2 py-0.5 text-sm font-bold text-primary-700 tabular-nums dark:bg-primary-950/50 dark:text-primary-300">{{ duration }} s</span>
        </div>

        <div class="mt-4 flex flex-wrap items-center gap-3">
          <button
            type="button"
            class="rounded-xl bg-gradient-to-br from-primary-500 to-primary-700 px-4 py-2.5 text-sm font-bold text-white shadow-sm transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="!canRun"
            data-testid="studio-bakeoff-run"
            @click="run"
          >
            {{ running ? t('studio.bakeoff.running') : t('studio.bakeoff.run', { count: selectedModelIds.length }) }}
          </button>
          <span class="rounded-lg bg-amber-50 px-2.5 py-1 text-xs font-medium text-amber-700 ring-1 ring-amber-200 dark:bg-amber-950/30 dark:text-amber-300 dark:ring-amber-900/50">
            {{ t('studio.bakeoff.totalCost', { cost: formatUsd(totalCost) }) }}
          </span>
          <span v-if="!canAfford" class="text-xs font-medium text-red-600 dark:text-red-400">
            {{ t('studio.bakeoff.cannotAfford') }}
            <router-link to="/purchase" class="underline">{{ t('studio.topUp') }}</router-link>
          </span>
        </div>

        <div v-if="errorMessage" class="mt-3 rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-800 dark:border-red-900/50 dark:bg-red-950/40 dark:text-red-100">
          {{ errorMessage }}
          <router-link v-if="errorCode === 'insufficient_balance'" to="/purchase" class="ml-1 font-medium underline">{{ t('studio.topUp') }}</router-link>
        </div>
      </template>
    </div>

    <!-- side-by-side panels -->
    <div v-if="panels.length" class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
      <div v-for="p in panels" :key="p.modelId" data-testid="bakeoff-panel" class="rounded-xl border border-gray-200 bg-white p-3 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <div class="mb-2 flex items-center justify-between">
          <span class="text-sm font-semibold text-gray-900 dark:text-white">{{ p.label }}</span>
          <span class="text-[11px] text-gray-400 dark:text-dark-500">{{ t('studio.via', { vendor: p.vendorLabel }) }}</span>
        </div>
        <!-- image -->
        <template v-if="modality === 'image'">
          <div v-if="p.src" class="overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700">
            <img :src="p.src" :alt="prompt" class="aspect-square w-full object-cover" loading="lazy" />
          </div>
          <div v-else class="flex aspect-square items-center justify-center rounded-lg bg-gray-50 text-xs text-gray-400 dark:bg-dark-800 dark:text-dark-500">
            <span v-if="p.state === 'processing'">{{ t('studio.bakeoff.generating') }}</span>
            <span v-else-if="p.state === 'failed'" class="text-red-500">{{ t('studio.bakeoff.failed') }}</span>
            <span v-else>—</span>
          </div>
        </template>
        <!-- video -->
        <template v-else>
          <div v-if="p.url" class="overflow-hidden rounded-lg bg-black">
            <video :src="p.url" controls class="aspect-video w-full"></video>
          </div>
          <div v-else class="flex aspect-video items-center justify-center rounded-lg bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-dark-400">
            <span v-if="p.state === 'processing'" class="inline-flex items-center gap-1.5"><span class="h-2 w-2 animate-pulse rounded-full bg-primary-500"></span>{{ t('studio.video.statusProcessing') }} {{ formatElapsed(p.elapsedS || 0) }}</span>
            <span v-else-if="p.state === 'failed'" class="text-red-500">{{ t('studio.bakeoff.failed') }}</span>
            <span v-else>—</span>
          </div>
        </template>
        <div class="mt-2 flex items-center justify-between text-sm">
          <span class="font-bold text-primary-700 dark:text-primary-300">{{ formatUsd(p.cost) }}</span>
          <span v-if="p.elapsedS != null && p.state !== 'idle'" class="text-xs text-gray-500 dark:text-dark-400">⏱ {{ p.elapsedS }}s</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { gatewayImageGenerations, gatewayVideoSubmit } from '@/api/playground'
import { extractImageItems, extractVideoTaskId, videoStateFromFetch, extractVideoUrl } from '@/constants/playgroundMedia.tk'
import {
  VIDEO_DURATION_MIN,
  VIDEO_DURATION_MAX,
  VIDEO_DURATION_DEFAULT,
  resolveAvailableModels,
  type StudioModality,
  type MediaPriceMap,
} from '@/constants/mediaTiers.tk'
import { estimateImageCost, estimateImageHoldCost, estimateVideoCost, formatUsd } from '@/utils/mediaCostEstimate.tk'
import { classifyGatewayError, studioErrorI18nKey, type StudioErrorCode } from '@/utils/studioGatewayError.tk'
import { useVideoTaskPoll } from '@/composables/useVideoTaskPoll'
import type { VideoTaskItem } from '@/composables/useMediaLibrary'
import type { ApiKey } from '@/types'

const props = defineProps<{
  apiKey: string
  gatewayBase: string
  availableIds: Set<string>
  priceMap: MediaPriceMap
  balance: number
  keyId: number | null
  keys: ApiKey[]
  rateMultiplier: number
}>()
const emit = defineEmits<{ (e: 'spent'): void }>()

const { t } = useI18n()

const MAX_PANELS = 4
const DEFAULT_IMAGE_SIZE = '1024x1024'

const modality = ref<StudioModality>('video')
const models = computed(() => resolveAvailableModels(modality.value, props.availableIds, props.priceMap))
const selectedModelIds = ref<string[]>([])
const prompt = ref('')
const duration = ref(VIDEO_DURATION_DEFAULT)
const running = ref(false)
const errorMessage = ref('')
const errorCode = ref<StudioErrorCode | ''>('')

interface BakePanel {
  /** modelId for selection identity; servedId is the billing key sent upstream. */
  modelId: string
  servedId: string
  label: string
  vendorLabel: string
  cost: number
  state: 'idle' | 'processing' | 'succeeded' | 'failed'
  src?: string
  taskId?: string
  url?: string
  elapsedS?: number
}
const panels = ref<BakePanel[]>([])

// Local VideoTaskItem store for the poll engine (ephemeral — Bake-Off is a
// comparison surface, not persistent history).
const videoTasks = ref<VideoTaskItem[]>([])
function patchVideoTask(id: string, patch: Partial<VideoTaskItem>): void {
  const idx = videoTasks.value.findIndex((tk) => tk.id === id)
  if (idx >= 0) {
    const next = [...videoTasks.value]
    next[idx] = { ...next[idx], ...patch }
    videoTasks.value = next
  }
  // Mirror onto the panel by taskId.
  const panel = panels.value.find((p) => p.taskId === id)
  if (panel) {
    if (patch.state) panel.state = patch.state
    if (patch.url != null) panel.url = patch.url
    if (patch.elapsedS != null) panel.elapsedS = patch.elapsedS
  }
}
const poll = useVideoTaskPoll({
  gatewayBase: () => props.gatewayBase,
  resolveKey: (keyId) => props.keys.find((k) => k.id === keyId)?.key,
  patch: patchVideoTask,
  onTerminal: () => emit('spent'),
})

function selectedResolved() {
  return models.value.filter((r) => selectedModelIds.value.includes(r.model.modelId))
}

const totalCost = computed(() =>
  selectedResolved().reduce((sum, r) => {
    if (modality.value === 'image') {
      return sum + estimateImageCost({ baseImagePrice: r.baseImagePrice || 0, size: DEFAULT_IMAGE_SIZE, n: 1, rateMultiplier: props.rateMultiplier })
    }
    return sum + estimateVideoCost({ perSecond: r.perSecond || 0, seconds: duration.value, rateMultiplier: props.rateMultiplier })
  }, 0)
)

// Affordability uses the HOLD upper bound (image: 4K tier-max), summed across panels.
const totalHold = computed(() =>
  selectedResolved().reduce((sum, r) => {
    if (modality.value === 'image') {
      return sum + estimateImageHoldCost({ baseImagePrice: r.baseImagePrice || 0, n: 1, rateMultiplier: props.rateMultiplier })
    }
    return sum + estimateVideoCost({ perSecond: r.perSecond || 0, seconds: duration.value, rateMultiplier: props.rateMultiplier })
  }, 0)
)
const canAfford = computed(() => totalHold.value <= props.balance)
const canRun = computed(
  () => !running.value && !!props.apiKey && !!prompt.value.trim() && selectedModelIds.value.length >= 2 && canAfford.value && props.keyId != null
)

function setModality(m: StudioModality): void {
  if (running.value) return
  modality.value = m
  selectedModelIds.value = []
  panels.value = []
  videoTasks.value = []
}

function toggleModel(id: string): void {
  if (running.value) return
  const i = selectedModelIds.value.indexOf(id)
  if (i >= 0) selectedModelIds.value.splice(i, 1)
  else if (selectedModelIds.value.length < MAX_PANELS) selectedModelIds.value.push(id)
}

function formatElapsed(s: number): string {
  const sec = Math.max(0, Math.round(s || 0))
  return `${String(Math.floor(sec / 60)).padStart(2, '0')}:${String(sec % 60).padStart(2, '0')}`
}

async function run(): Promise<void> {
  const text = prompt.value.trim()
  const chosen = selectedResolved()
  if (!text || chosen.length < 2 || running.value || !canAfford.value || props.keyId == null) return
  errorMessage.value = ''
  errorCode.value = ''
  running.value = true
  poll.stopAll()
  videoTasks.value = []
  // Seed panels.
  panels.value = chosen.map((r) => ({
    modelId: r.model.modelId,
    servedId: r.servedId,
    label: r.model.displayName,
    vendorLabel: r.model.vendorLabel,
    cost:
      modality.value === 'image'
        ? estimateImageCost({ baseImagePrice: r.baseImagePrice || 0, size: DEFAULT_IMAGE_SIZE, n: 1, rateMultiplier: props.rateMultiplier })
        : estimateVideoCost({ perSecond: r.perSecond || 0, seconds: duration.value, rateMultiplier: props.rateMultiplier }),
    state: 'processing',
  }))

  try {
    if (modality.value === 'image') {
      await Promise.all(
        panels.value.map(async (panel) => {
          try {
            const raw = await gatewayImageGenerations(props.apiKey, props.gatewayBase, { model: panel.servedId, prompt: text, size: DEFAULT_IMAGE_SIZE, n: 1 })
            const items = extractImageItems(raw)
            if (!items.length) throw new Error('no_image')
            panel.src = items[0].src
            panel.state = 'succeeded'
          } catch (e) {
            panel.state = 'failed'
            mapError(e)
          }
        })
      )
      emit('spent')
    } else {
      // Submit all videos, then poll each.
      await Promise.all(
        panels.value.map(async (panel) => {
          try {
            const raw = await gatewayVideoSubmit(props.apiKey, props.gatewayBase, { model: panel.servedId, prompt: text, duration: duration.value })
            const taskId = extractVideoTaskId(raw)
            if (!taskId) throw new Error('no_task')
            panel.taskId = taskId
            const state = videoStateFromFetch(raw)
            panel.state = state
            panel.url = state === 'succeeded' ? extractVideoUrl(raw) : ''
            const task: VideoTaskItem = {
              id: taskId,
              prompt: text,
              model: panel.servedId,
              vendorLabel: panel.vendorLabel,
              seconds: duration.value,
              estCost: panel.cost,
              keyId: props.keyId as number,
              state,
              url: panel.url || '',
              submittedAtMs: Date.now(),
              elapsedS: 0,
            }
            videoTasks.value = [...videoTasks.value, task]
            if (state === 'processing') poll.resume(task)
          } catch (e) {
            panel.state = 'failed'
            mapError(e)
          }
        })
      )
      emit('spent') // holds reserved
    }
  } finally {
    running.value = false
  }
}

function mapError(e: unknown): void {
  const msg = e instanceof Error ? e.message : ''
  const code = classifyGatewayError(msg)
  if (code !== 'generic') {
    errorCode.value = code
    errorMessage.value = t(studioErrorI18nKey(code))
  } else if (!errorMessage.value && msg && msg !== 'no_image' && msg !== 'no_task') {
    errorMessage.value = msg
  }
}
</script>
