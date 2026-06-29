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

        <!-- Shared duration is a TARGET across the compared models; chips are the
             UNION of the selected models' accepted durations and each panel snaps
             to its own model's nearest valid value (Veo 4/6/8 vs Seedance 5/10
             share none), so no panel is ever submitted an out-of-range duration. -->
        <div v-if="modality === 'video' && durationOptions.length" class="mt-3 flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('studio.video.duration') }}</span>
          <button
            v-for="d in durationOptions"
            :key="d"
            type="button"
            class="rounded-lg border px-3 py-1.5 text-sm font-medium tabular-nums transition disabled:cursor-not-allowed disabled:opacity-50"
            :class="duration === d ? 'border-primary-600 bg-primary-600 text-white' : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-dark-300'"
            :disabled="running"
            @click="duration = d"
          >
            {{ d }} s
          </button>
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
          <!-- On-demand playback only: no always-on <video> (it shows a poster-less
               black box pre-play and would race up to MAX_PANELS competing loads).
               Poster tile → in-page lightbox, mirroring VideoStudio and sharing
               videoPlaybackUrl so inline data:video plays tab-local without rehosting. -->
          <button
            v-if="p.url"
            type="button"
            class="group relative block aspect-video w-full overflow-hidden rounded-lg bg-gradient-to-br from-gray-800 to-gray-950"
            data-testid="bakeoff-video-play"
            :title="t('studio.video.play')"
            @click="openVideoPreview(p)"
          >
            <span class="pointer-events-none absolute inset-0 flex items-center justify-center">
              <span class="flex h-12 w-12 items-center justify-center rounded-full bg-white/90 shadow-lg transition group-hover:scale-110 group-hover:bg-white">
                <span aria-hidden="true" class="ml-1 text-xl text-gray-900">▶</span>
              </span>
            </span>
          </button>
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

    <!-- In-page video lightbox: plays one panel on demand (http URL direct, inline
         data:video as a tab-local Blob via the shared videoPlaybackUrl) instead of
         rendering MAX_PANELS always-on <video> elements. -->
    <Teleport to="body">
      <div
        v-if="videoPreview"
        class="fixed inset-0 z-[100] flex flex-col bg-black/85 backdrop-blur-sm"
        data-testid="bakeoff-video-preview"
        @click.self="closeVideoPreview"
      >
        <div class="flex items-center justify-end p-3">
          <button type="button" class="rounded-lg bg-white/10 px-3 py-1.5 text-sm font-medium text-white hover:bg-white/20" data-testid="bakeoff-video-preview-close" @click="closeVideoPreview">
            {{ t('studio.video.close') }} ✕
          </button>
        </div>
        <div class="flex min-h-0 flex-1 items-center justify-center px-4" @click.self="closeVideoPreview">
          <video v-if="videoPreviewUrl" :src="videoPreviewUrl" controls autoplay playsinline class="max-h-full max-w-full rounded-lg bg-black shadow-2xl"></video>
        </div>
        <div class="flex flex-wrap items-center justify-center gap-3 p-4">
          <span class="max-w-[60vw] truncate text-xs text-white/80" :title="videoPreview.label">{{ videoPreview.label }}</span>
          <span class="shrink-0 rounded bg-white/15 px-1.5 py-0.5 text-[11px] font-semibold text-white">{{ formatUsd(videoPreview.cost) }}</span>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gatewayGeminiImageViaChat, gatewayImageGenerations, gatewayVideoSubmit } from '@/api/playground'
import {
  extractChatImageItems,
  extractImageItems,
  extractVideoTaskId,
  isGeminiNativeImageModel,
  videoStateFromFetch,
  extractVideoUrl,
} from '@/constants/playgroundMedia.tk'
import {
  VIDEO_DURATION_DEFAULT,
  videoDurationDefault,
  snapVideoDuration,
  resolveAvailableModels,
  type StudioModality,
  type MediaPriceMap,
} from '@/constants/mediaTiers.tk'
import { estimateImageCost, estimateImageHoldCost, estimateVideoCost, formatUsd } from '@/utils/mediaCostEstimate.tk'
import { videoPlaybackUrl } from '@/utils/studioMedia.tk'
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
/** Imagen / seedream: ratio code on /v1/images/generations (see ImageStudio sentSize). */
const DEFAULT_IMAGEN_SIZE = '1:1'
/** Gemini-native image: aspect_ratio via /v1/chat/completions extra_body.google.image_config. */
const DEFAULT_GEMINI_ASPECT = '1:1'

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
  /** Video only: this model's snapped duration (seconds) actually submitted. */
  seconds?: number
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

// ---- In-page video lightbox (on-demand playback; no always-on <video>) ----------
const videoPreview = ref<BakePanel | null>(null)
const videoPreviewUrl = ref('')
let videoPreviewRevoke: () => void = () => {}

function openVideoPreview(panel: BakePanel): void {
  if (!panel.url) return
  videoPreviewRevoke()
  // http(s) upstream URL plays directly; inline data:video becomes a tab-local Blob
  // (shared with VideoStudio via videoPlaybackUrl — TokenKey never rehosts the clip).
  const playback = videoPlaybackUrl(panel.url)
  videoPreviewRevoke = playback.revoke
  videoPreview.value = panel
  videoPreviewUrl.value = playback.url
}

function closeVideoPreview(): void {
  videoPreviewRevoke()
  videoPreviewRevoke = () => {}
  videoPreview.value = null
  videoPreviewUrl.value = ''
}

onBeforeUnmount(closeVideoPreview)

function selectedResolved() {
  return models.value.filter((r) => selectedModelIds.value.includes(r.model.modelId))
}

// Union of the selected video models' accepted durations (sorted, deduped) →
// the shared chip options. Each panel later snaps this target to its own model.
const durationOptions = computed<number[]>(() => {
  if (modality.value !== 'video') return []
  const set = new Set<number>()
  for (const r of selectedResolved()) for (const d of r.model.videoDurations ?? []) set.add(d)
  return [...set].sort((a, b) => a - b)
})

// Keep the shared target inside the union; default to its MAX (user directive).
watch(durationOptions, (opts) => {
  if (opts.length && !opts.includes(duration.value)) {
    duration.value = videoDurationDefault(opts)
  }
})

/** Per-panel duration: the shared target snapped to THIS model's accepted set. */
function panelSeconds(r: { model: { videoDurations?: number[] } }): number {
  return snapVideoDuration(duration.value, r.model.videoDurations)
}

const totalCost = computed(() =>
  selectedResolved().reduce((sum, r) => {
    if (modality.value === 'image') {
      return sum + estimateImageCost({ baseImagePrice: r.baseImagePrice || 0, size: DEFAULT_IMAGEN_SIZE, n: 1, rateMultiplier: props.rateMultiplier })
    }
    return sum + estimateVideoCost({ perSecond: r.perSecond || 0, seconds: panelSeconds(r), rateMultiplier: props.rateMultiplier })
  }, 0)
)

// Affordability uses the HOLD upper bound (image: 4K tier-max), summed across panels.
const totalHold = computed(() =>
  selectedResolved().reduce((sum, r) => {
    if (modality.value === 'image') {
      return sum + estimateImageHoldCost({ baseImagePrice: r.baseImagePrice || 0, n: 1, rateMultiplier: props.rateMultiplier })
    }
    return sum + estimateVideoCost({ perSecond: r.perSecond || 0, seconds: panelSeconds(r), rateMultiplier: props.rateMultiplier })
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
        ? estimateImageCost({ baseImagePrice: r.baseImagePrice || 0, size: DEFAULT_IMAGEN_SIZE, n: 1, rateMultiplier: props.rateMultiplier })
        : estimateVideoCost({ perSecond: r.perSecond || 0, seconds: panelSeconds(r), rateMultiplier: props.rateMultiplier }),
    seconds: modality.value === 'video' ? panelSeconds(r) : undefined,
    state: 'processing',
  }))

  try {
    if (modality.value === 'image') {
      await Promise.all(
        panels.value.map(async (panel) => {
          try {
            const items = await generateBakeoffImage(panel.servedId, text)
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
            const raw = await gatewayVideoSubmit(props.apiKey, props.gatewayBase, { model: panel.servedId, prompt: text, duration: panel.seconds })
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
              seconds: panel.seconds ?? duration.value,
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

/** Route like ImageStudio: gemini-native via chat; imagen/seedream via /v1/images/generations. */
async function generateBakeoffImage(modelId: string, prompt: string) {
  if (isGeminiNativeImageModel(modelId)) {
    const raw = await gatewayGeminiImageViaChat(props.apiKey, props.gatewayBase, {
      model: modelId,
      prompt,
      aspectRatio: DEFAULT_GEMINI_ASPECT,
    })
    return extractChatImageItems(raw)
  }
  const raw = await gatewayImageGenerations(props.apiKey, props.gatewayBase, {
    model: modelId,
    prompt,
    size: DEFAULT_IMAGEN_SIZE,
    n: 1,
  })
  return extractImageItems(raw)
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
