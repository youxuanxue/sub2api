<template>
  <div class="grid grid-cols-1 gap-5 lg:grid-cols-[360px_1fr]">
    <!-- LEFT: orchestration -->
    <div class="space-y-4">
      <div v-if="models.length === 0" class="rounded-xl border border-dashed border-gray-300 bg-white/60 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400">
        {{ t('studio.video.modelEmpty') }}
        <router-link class="mt-1 block font-medium text-primary-600 underline dark:text-primary-400" to="/pricing">
          {{ t('studio.viewPricing') }}
        </router-link>
      </div>

      <!-- Transparent MODEL picker (friendly name + price + vendor + raw id subtext). -->
      <div v-else class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <div class="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ t('studio.video.modelLabel') }}</div>
        <div class="space-y-2">
          <button
            v-for="r in models"
            :key="r.model.modelId"
            type="button"
            class="w-full rounded-xl border p-3 text-left transition"
            :class="selectedModelId === r.model.modelId
              ? 'border-primary-500 bg-primary-50 ring-2 ring-primary-500/30 dark:border-primary-500 dark:bg-primary-950/40'
              : 'border-gray-200 hover:border-primary-300 dark:border-dark-600'"
            data-testid="studio-video-model"
            @click="selectedModelId = r.model.modelId"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-[13px] font-semibold text-gray-900 dark:text-white">{{ r.model.displayName }}</span>
              <span class="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-600 dark:bg-dark-800 dark:text-dark-300">{{ t(r.model.qualityBadgeKey) }}</span>
            </div>
            <div class="mt-1 flex items-center justify-between gap-2">
              <span class="text-[12px] font-bold text-primary-700 dark:text-primary-300">{{ formatUsd(r.perSecond || 0) }}{{ t('studio.video.perSecondUnit') }}</span>
              <span class="text-[10px] text-gray-400 dark:text-dark-500">{{ t('studio.via', { vendor: r.model.vendorLabel }) }}</span>
            </div>
            <div class="mt-0.5 truncate font-mono text-[10px] text-gray-400 dark:text-dark-500" :title="r.servedId">{{ r.servedId }}</div>
            <div v-if="r.model.needsApikeyAccount" class="mt-1 text-[10px] font-medium text-amber-600 dark:text-amber-400">{{ t('studio.needsApikeyAccount') }}</div>
          </button>
        </div>
      </div>

      <div v-if="models.length" class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <textarea
          v-model="prompt"
          rows="3"
          class="w-full resize-none rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
          :placeholder="t('studio.video.promptPlaceholder')"
          :disabled="sending"
          @input="userEditedPrompt = true"
        />
        <div class="mt-3">
          <div class="mb-1.5 text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('studio.video.duration') }}</div>
          <!-- Per-model DISCRETE durations (chips) — the model declares exactly the
               seconds its upstream accepts, so we never offer (or quote) an
               out-of-range value that would hard-fail on submit. -->
          <div class="flex flex-wrap gap-2 text-sm" data-testid="studio-video-duration">
            <button
              v-for="d in durations"
              :key="d"
              type="button"
              class="rounded-lg border px-3 py-1.5 font-medium tabular-nums transition disabled:cursor-not-allowed disabled:opacity-50"
              :class="duration === d ? 'border-primary-600 bg-primary-600 text-white' : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-dark-300'"
              :disabled="sending"
              @click="duration = d"
            >
              {{ d }} s
            </button>
          </div>
        </div>
        <div class="mt-3">
          <div class="mb-1.5 text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('studio.video.aspect') }}</div>
          <div class="flex flex-wrap gap-2 text-sm">
            <button
              type="button"
              class="rounded-lg border px-3 py-1.5 font-medium transition"
              :class="aspectId === '' ? 'border-primary-600 bg-primary-600 text-white' : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-dark-300'"
              @click="aspectId = ''"
            >
              {{ t('studio.video.aspectAuto') }}
            </button>
            <button
              v-for="p in VIDEO_ASPECT_PRESETS"
              :key="p.id"
              type="button"
              class="rounded-lg border px-3 py-1.5 font-medium transition"
              :class="aspectId === p.id ? 'border-primary-600 bg-primary-600 text-white' : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-dark-300'"
              @click="aspectId = p.id"
            >
              {{ p.label }}
            </button>
          </div>
        </div>

        <!-- Advanced: only params the SELECTED model actually honors are rendered. -->
        <template v-if="selected && selected.model.supportedParams.length">
          <button
            type="button"
            class="mt-3 flex items-center gap-1 text-xs font-medium text-primary-600 dark:text-primary-300"
            data-testid="studio-video-advanced-toggle"
            @click="showAdvanced = !showAdvanced"
          >
            {{ t('studio.advanced.toggle') }} <span>{{ showAdvanced ? '▴' : '▾' }}</span>
          </button>
          <div v-if="showAdvanced" class="mt-2 space-y-3 rounded-lg border border-dashed border-gray-200 p-3 dark:border-dark-700">
            <div v-if="supports('negativePrompt')">
              <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400">{{ t('studio.advanced.negativePrompt') }}</label>
              <input v-model="negativePrompt" type="text" :placeholder="t('studio.advanced.negativePromptHint')" class="w-full rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-sm text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white" />
            </div>
            <div v-if="supports('firstFrameImage')">
              <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400">{{ t('studio.advanced.firstFrame') }}</label>
              <input v-model="firstFrameImage" type="text" :placeholder="t('studio.advanced.firstFrameHint')" class="w-full rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-sm text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white" />
            </div>
            <div v-if="supports('seed')">
              <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-400">{{ t('studio.advanced.seed') }}</label>
              <input v-model.number="seed" type="number" :min="SEED_MIN" :max="SEED_MAX" :placeholder="t('studio.advanced.seedHint')" class="w-full rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-sm text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white" />
            </div>
          </div>
        </template>
      </div>

      <!-- Cost + primary CTA. Moved out of a dedicated right column into the
           orchestration stack so the action lives where composing ends — one
           vertical sweep (model → prompt → params → cost → Generate) instead of
           an eye-jump across the results gallery to a far-right button. -->
      <div v-if="models.length" class="space-y-4">
        <div class="rounded-xl border border-primary-200 bg-primary-50/40 p-4 shadow-sm dark:border-primary-900/40 dark:bg-primary-950/30">
          <div class="text-xs font-semibold uppercase tracking-wide text-primary-700 dark:text-primary-300">{{ t('studio.cost.thisVideo') }}</div>
          <div class="mt-2 font-mono text-[12px] text-gray-600 dark:text-dark-300">{{ formula }}</div>
          <div class="mt-3 space-y-1 border-t border-primary-200/60 pt-3 text-sm dark:border-primary-900/40">
            <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.estimate') }}</span><span class="font-bold text-gray-900 tabular-nums dark:text-white">{{ formatUsd(estimate) }}</span></div>
            <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.balance') }}</span><span class="tabular-nums" :class="canAfford ? 'text-gray-700 dark:text-dark-200' : 'text-red-600 dark:text-red-400'">{{ formatUsd(balance) }} → {{ formatUsd(balance - estimate) }}</span></div>
          </div>
          <div class="mt-3 flex items-center gap-1.5 rounded-lg bg-green-50 px-2.5 py-2 text-[12px] font-medium text-green-700 ring-1 ring-green-200 dark:bg-green-950/30 dark:text-green-300 dark:ring-green-900/50">
            ✓ {{ t('studio.video.refundLine') }}
          </div>
        </div>

        <button
          type="button"
          class="w-full rounded-xl bg-gradient-to-br from-primary-500 to-primary-700 px-4 py-3 text-sm font-bold text-white shadow-sm transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          :disabled="!canGenerate"
          data-testid="studio-video-generate"
          @click="generate"
        >
          <template v-if="sending">{{ t('studio.video.submitting') }}</template>
          <template v-else-if="!canAfford">{{ t('studio.video.generateTopUp', { cost: formatUsd(estimate) }) }}</template>
          <template v-else>{{ t('studio.video.generate', { cost: formatUsd(estimate) }) }}</template>
        </button>
        <router-link
          v-if="!canAfford"
          to="/purchase"
          class="block text-center text-xs font-medium text-primary-600 underline dark:text-primary-400"
        >
          {{ t('studio.topUp') }}
        </router-link>

        <div
          v-if="errorMessage"
          class="rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-800 dark:border-red-900/50 dark:bg-red-950/40 dark:text-red-100"
          data-testid="studio-video-error"
        >
          {{ errorMessage }}
          <router-link v-if="errorCode === 'insufficient_balance'" to="/purchase" class="ml-1 font-medium underline">{{ t('studio.topUp') }}</router-link>
        </div>
      </div>
    </div>

    <!-- CENTER: task stack -->
    <div class="space-y-3">
      <!-- Results header + bulk clear (matches the image surface). Shown only when
           there is history; the per-card status badge is the source of truth for
           each task's state — there is no global completion banner to go stale. -->
      <div v-if="library.videoTasks.value.length" class="flex items-center justify-between px-1">
        <span class="text-sm font-semibold text-gray-700 dark:text-dark-200">{{ t('studio.video.resultsTitle') }}</span>
        <button type="button" class="text-xs text-gray-400 hover:text-gray-700 dark:hover:text-dark-200" data-testid="studio-video-clear-all" @click="clearAll">{{ t('studio.clear') }}</button>
      </div>

      <div v-if="!library.videoTasks.value.length" class="rounded-xl border border-gray-200 bg-white py-16 text-center text-sm text-gray-500 shadow-sm dark:border-dark-700 dark:bg-dark-900 dark:text-dark-400">
        {{ t('studio.video.emptyHint') }}
      </div>

      <div
        v-for="task in library.videoTasks.value"
        :key="task.id"
        class="rounded-xl border bg-white p-4 shadow-sm dark:bg-dark-900"
        :class="task.state === 'failed' ? 'border-red-200 dark:border-red-900/50' : 'border-gray-200 dark:border-dark-700'"
      >
        <div class="mb-2 flex flex-wrap items-center justify-between gap-2 text-xs">
          <span class="font-mono text-gray-400 dark:text-dark-500">{{ task.id }} · {{ task.model }} · {{ task.seconds }}s{{ task.aspectRatio ? ' · ' + task.aspectRatio : '' }}</span>
          <span
            class="rounded-full px-2 py-0.5 font-medium"
            :class="{
              'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300': task.state === 'processing',
              'bg-green-50 text-green-700 dark:bg-green-950/40 dark:text-green-300': task.state === 'succeeded',
              'bg-red-100 text-red-700 dark:bg-red-950/40 dark:text-red-300': task.state === 'failed'
            }"
          >
            <template v-if="task.state === 'processing'">{{ t('studio.video.statusProcessing') }}</template>
            <template v-else-if="task.state === 'succeeded'">{{ t('studio.video.statusSucceeded') }}</template>
            <template v-else>{{ t('studio.video.statusRefunded', { cost: formatUsd(task.estCost) }) }}</template>
          </span>
        </div>

        <!-- processing: timeline -->
        <div v-if="task.state === 'processing'" class="space-y-2">
          <div class="space-y-1 text-sm">
            <div class="flex items-center gap-2 text-gray-700 dark:text-dark-200"><span aria-hidden="true" class="text-primary-600">⬤</span> {{ t('studio.video.stepSubmitted') }} <span aria-hidden="true" class="text-green-600">✓</span></div>
            <div class="flex items-center gap-2 text-gray-700 dark:text-dark-200"><span aria-hidden="true" class="text-primary-600">◔</span> {{ t('studio.video.stepGenerating') }} <span class="ml-auto font-mono text-gray-400 tabular-nums dark:text-dark-500">{{ formatElapsed(task.elapsedS) }}</span></div>
            <div class="flex items-center gap-2 text-gray-400 dark:text-dark-500"><span aria-hidden="true">○</span> {{ t('studio.video.stepReady') }}</div>
          </div>
          <!-- Honest INDETERMINATE progress: the upstream render time is non-
               deterministic, so we animate "working" rather than assert a
               fabricated percentage the upstream can't keep. -->
          <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-100 dark:bg-dark-800" role="progressbar" :aria-label="t('studio.video.stepGenerating')">
            <div class="tk-indeterminate h-full w-2/5 rounded-full bg-gradient-to-r from-primary-400 to-primary-600"></div>
          </div>
          <div class="flex items-center justify-between gap-2 text-[11px]">
            <span class="text-gray-500 dark:text-dark-400">{{ t('studio.video.reserved', { cost: formatUsd(task.estCost) }) }} · {{ t('studio.video.usuallyTakes') }}</span>
            <button v-if="notifyState !== 'granted'" type="button" class="shrink-0 font-medium text-primary-600 dark:text-primary-300" @click="enableNotify">{{ notifyState === 'denied' ? t('studio.video.notifyDenied') : t('studio.video.notifyMe') }}</button>
            <span v-else class="shrink-0 font-medium text-green-600 dark:text-green-400">✓ {{ t('studio.video.notifyEnabled') }}</span>
          </div>
          <p class="truncate text-[11px] text-gray-400 dark:text-dark-500" :title="task.prompt">{{ task.prompt }}</p>
          <!-- Surfaced when the poll loop can no longer make progress (e.g. the API
               key was deleted mid-flight) so a dead task doesn't spin forever. -->
          <p v-if="task.errorMessage" class="rounded-lg bg-amber-50 px-2.5 py-1.5 text-[11px] font-medium text-amber-700 dark:bg-amber-950/40 dark:text-amber-300">{{ t('studio.video.stalled') }}</p>
        </div>

        <!-- succeeded: poster tile when playback is still available in this tab;
             otherwise prompt-only (upstream http links are not replayed after reload). -->
        <div v-else-if="task.state === 'succeeded'" class="space-y-2">
          <template v-if="videoTaskPlaybackAvailable(task)">
            <button
              type="button"
              class="group relative block w-full overflow-hidden rounded-lg bg-gradient-to-br from-gray-800 to-gray-950"
              :style="{ aspectRatio: posterAspect(task) }"
              :title="t('studio.video.playHint')"
              :aria-label="t('studio.video.play')"
              data-testid="studio-video-play"
              @click="openPreview(task)"
            >
              <span class="pointer-events-none absolute inset-0 flex items-center justify-center">
                <span class="flex h-14 w-14 items-center justify-center rounded-full bg-white/90 shadow-lg transition group-hover:scale-110 group-hover:bg-white">
                  <span aria-hidden="true" class="ml-1 text-2xl text-gray-900">▶</span>
                </span>
              </span>
              <span class="pointer-events-none absolute bottom-2 left-2 rounded bg-black/55 px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-white/90">{{ task.seconds }}s{{ task.aspectRatio ? ' · ' + task.aspectRatio : '' }}</span>
            </button>
            <p class="text-[10px] text-gray-400 dark:text-dark-500">{{ t('studio.video.retentionHint') }}</p>
          </template>
          <div
            v-else
            class="rounded-lg border border-dashed border-gray-200 bg-gray-50 px-3 py-4 dark:border-dark-600 dark:bg-dark-800/60"
            data-testid="studio-video-expired"
          >
            <p class="whitespace-pre-wrap break-words text-sm text-gray-800 dark:text-dark-100">{{ task.prompt }}</p>
            <p class="mt-2 text-[11px] text-gray-400 dark:text-dark-500">
              {{ task.urlExpired || !task.url ? t('studio.video.expiredReload') : t('studio.video.noUrlHint') }}
            </p>
          </div>
          <div class="flex items-center justify-between text-[11px] text-gray-500 dark:text-dark-400">
            <span v-if="videoTaskPlaybackAvailable(task)" class="truncate" :title="task.prompt">{{ task.prompt }}</span>
            <span class="shrink-0 rounded bg-primary-50 px-1.5 py-0.5 font-semibold text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">{{ formatUsd(task.estCost) }}</span>
          </div>
          <div class="flex gap-3 text-[11px] font-medium text-gray-500 dark:text-dark-400">
            <button v-if="videoTaskPlaybackAvailable(task)" type="button" class="text-primary-600 dark:text-primary-300" @click="openPreview(task)">{{ t('studio.video.play') }}</button>
            <button type="button" @click="reuse(task)">{{ t('studio.image.usePrompt') }}</button>
            <button type="button" @click="removeTask(task.id)">{{ t('studio.clear') }}</button>
          </div>
        </div>

        <!-- failed: refund -->
        <div v-else class="space-y-1">
          <p class="text-[13px] text-red-800 dark:text-red-200">{{ t('studio.video.failedRefunded', { cost: formatUsd(task.estCost) }) }}</p>
          <details v-if="task.errorMessage" class="text-[11px] text-gray-400 dark:text-dark-500">
            <summary class="cursor-pointer">{{ t('studio.video.techDetails') }}</summary>
            <pre class="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-2 dark:bg-dark-950">{{ task.errorMessage }}</pre>
          </details>
          <button type="button" class="text-[11px] text-gray-400 hover:text-gray-700 dark:hover:text-dark-200" @click="removeTask(task.id)">{{ t('studio.clear') }}</button>
        </div>
      </div>
    </div>

    <!-- Lightbox: in-page playback (replaces the always-on black inline player and
         the open-in-new-tab dead-end that 404'd for data: veo clips). HTTP video
         URLs go straight to the browser; inline data:video results become a
         temporary Blob URL for this tab only. -->
    <Teleport to="body">
      <div
        v-if="preview"
        class="fixed inset-0 z-[100] flex flex-col bg-black/85 backdrop-blur-sm"
        data-testid="studio-video-preview"
        @click.self="closePreview"
      >
        <div class="flex items-center justify-end p-3">
          <button type="button" class="rounded-lg bg-white/10 px-3 py-1.5 text-sm font-medium text-white hover:bg-white/20" data-testid="studio-video-preview-close" :aria-label="t('studio.video.close')" @click="closePreview">
            {{ t('studio.video.close') }} ✕
          </button>
        </div>
        <div class="flex min-h-0 flex-1 items-center justify-center px-4" @click.self="closePreview">
          <video
            v-if="previewState === 'ready' && previewUrl"
            :src="previewUrl"
            controls
            autoplay
            playsinline
            class="max-h-full max-w-full rounded-lg bg-black shadow-2xl"
            @error="onPreviewError"
          ></video>
          <div v-else-if="previewState === 'loading'" class="text-sm text-white/80">{{ t('studio.video.loadingPreview') }}</div>
          <div v-else class="max-w-sm rounded-xl bg-white/10 p-6 text-center">
            <p class="text-sm font-semibold text-white">{{ t('studio.video.expiredTitle') }}</p>
            <p class="mt-1 text-xs text-white/70">{{ t('studio.video.expiredHint') }}</p>
            <div class="mt-3 flex items-center justify-center gap-2">
              <!-- A media error can be transient (one-off network blip), so offer a
                   retry before pushing the user to (re-)pay to regenerate. -->
              <button type="button" class="rounded-md bg-white px-3 py-1.5 text-[12px] font-medium text-gray-900 hover:bg-gray-100" @click="retryPreview">{{ t('studio.video.retry') }}</button>
              <button type="button" class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white" @click="reuseAndClose(preview)">{{ t('studio.image.usePrompt') }}</button>
            </div>
          </div>
        </div>
        <div class="flex flex-wrap items-center justify-center gap-3 p-4">
          <span class="max-w-[60vw] truncate text-xs text-white/80" :title="preview.prompt">{{ preview.prompt }}</span>
          <span class="shrink-0 rounded bg-white/15 px-1.5 py-0.5 text-[11px] font-semibold text-white">{{ formatUsd(preview.estCost) }}</span>
          <button v-if="previewState === 'ready' && previewUrl" type="button" class="rounded-md bg-white px-3 py-1.5 text-[12px] font-medium text-gray-900 hover:bg-gray-100" @click="downloadMedia(previewUrl, `tokenkey-${preview.id}.mp4`)">{{ t('studio.video.download') }}</button>
          <!-- Restore a way to grab the link itself (the #860 rework dropped the
               open-in-new-tab anchor → the "看不到链接" report). -->
          <button v-if="previewState === 'ready' && previewUrl" type="button" class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white" data-testid="studio-video-copy-link" @click="copyPreviewLink">{{ copied ? t('studio.video.copied') : t('studio.video.copyLink') }}</button>
          <button type="button" class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white" @click="reuseAndClose(preview)">{{ t('studio.image.usePrompt') }}</button>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gatewayVideoSubmit } from '@/api/playground'
import { extractVideoTaskId, videoStateFromFetch, extractVideoUrl } from '@/constants/playgroundMedia.tk'
import {
  VIDEO_ASPECT_PRESETS,
  VIDEO_DURATION_DEFAULT,
  videoDurationDefault,
  SEED_MIN,
  SEED_MAX,
  resolveAvailableModels,
  defaultModelId,
  type StudioParam,
  type MediaPriceMap,
} from '@/constants/mediaTiers.tk'
import { estimateVideoCost, formatUsd } from '@/utils/mediaCostEstimate.tk'
import { downloadMedia } from '@/utils/studioDownload.tk'
import { videoPlaybackUrl, videoTaskPlaybackAvailable } from '@/utils/studioMedia.tk'
import { classifyGatewayError, studioErrorI18nKey, type StudioErrorCode } from '@/utils/studioGatewayError.tk'
import { useMediaLibrary, type VideoTaskItem } from '@/composables/useMediaLibrary'
import { useVideoTaskPoll, requestVideoNotifyPermission, maybeNotify } from '@/composables/useVideoTaskPoll'
import type { ApiKey } from '@/types'

const props = defineProps<{
  apiKey: string
  gatewayBase: string
  availableIds: Set<string>
  priceMap: MediaPriceMap
  balance: number
  userId: number | string
  keyId: number | null
  keys: ApiKey[]
  rateMultiplier: number
}>()
const emit = defineEmits<{ (e: 'spent'): void }>()

const { t } = useI18n()
const library = useMediaLibrary(props.userId)

const models = computed(() => resolveAvailableModels('video', props.availableIds, props.priceMap))
const selectedModelId = ref<string>('')
const selected = computed(() => models.value.find((r) => r.model.modelId === selectedModelId.value) ?? null)
const supports = (p: StudioParam): boolean => !!selected.value?.model.supportedParams.includes(p)

// The selected model's accepted durations (chips); default lands on the MAX.
const durations = computed<number[]>(() => selected.value?.model.videoDurations ?? [VIDEO_DURATION_DEFAULT])
const duration = ref<number>(VIDEO_DURATION_DEFAULT)
const aspectId = ref<string>('') // '' = auto (no aspect_ratio sent — proven zero-extra-field path)
const prompt = ref('')
const userEditedPrompt = ref(false)
const sending = ref(false)
const errorMessage = ref('')
const errorCode = ref<StudioErrorCode | ''>('')

// Notification-permission state (NOT a per-task event), so a deliberate "notify me"
// click can confirm itself on the card without a global banner that goes stale
// across resubmits — the bug the old `lastEvent` toast had.
const notifyState = ref<'idle' | 'granted' | 'denied'>('idle')

// Advanced (optional; only sent when set).
const showAdvanced = ref(false)
const seed = ref<number | null>(null)
const negativePrompt = ref('')
const firstFrameImage = ref('')

const estimate = computed(() => {
  if (!selected.value) return 0
  return estimateVideoCost({
    perSecond: selected.value.perSecond || 0,
    seconds: duration.value,
    rateMultiplier: props.rateMultiplier,
  })
})
const canAfford = computed(() => estimate.value <= props.balance)
const canGenerate = computed(
  () => !sending.value && !!props.apiKey && !!prompt.value.trim() && !!selected.value && canAfford.value
)
const formula = computed(() => {
  if (!selected.value) return ''
  return t('studio.video.formula', { rate: formatUsd(selected.value.perSecond || 0), seconds: duration.value })
})

const poll = useVideoTaskPoll({
  gatewayBase: () => props.gatewayBase,
  resolveKey: (keyId) => props.keys.find((k) => k.id === keyId)?.key,
  patch: library.patchVideoTask,
  onTerminal: (task, state) => {
    emit('spent')
    // The per-card status badge IS the in-page completion signal (no banner to go
    // stale). For a user who walked away, fire a best-effort browser notification.
    if (state === 'succeeded' && task.url) {
      maybeNotify(t('studio.video.notifyTitle'), task.prompt)
    }
  },
})

function applySamplePrompt(): void {
  if (userEditedPrompt.value) return
  prompt.value = t('studio.video.samplePrompt')
}
watch(
  models,
  (list) => {
    if (!list.some((r) => r.model.modelId === selectedModelId.value)) {
      selectedModelId.value = defaultModelId(list) ?? ''
    }
  },
  { immediate: true }
)
watch(
  selected,
  () => {
    applySamplePrompt()
    // Keep `duration` valid for the selected model: default to the MAX accepted
    // value, and snap any stale selection back into the model's allowed set so
    // the estimate/quote is never for an out-of-range (guaranteed-fail) duration.
    if (!durations.value.includes(duration.value)) {
      duration.value = videoDurationDefault(selected.value?.model.videoDurations)
    }
  },
  { immediate: true }
)

function formatElapsed(s: number): string {
  const sec = Math.max(0, Math.round(s || 0))
  const m = String(Math.floor(sec / 60)).padStart(2, '0')
  const ss = String(sec % 60).padStart(2, '0')
  return `${m}:${ss}`
}

// CSS aspect-ratio for the poster tile from the task's stored ratio ("16:9" →
// "16 / 9"); default to 16:9 when the request used auto (no ratio recorded).
function posterAspect(task: VideoTaskItem): string {
  const a = (task.aspectRatio || '').replace(':', ' / ').trim()
  return /^\d+\s*\/\s*\d+$/.test(a) ? a : '16 / 9'
}

function reuse(task: VideoTaskItem): void {
  prompt.value = task.prompt
  userEditedPrompt.value = true
}

function removeTask(id: string): void {
  // Stop the poll loop BEFORE dropping the task, otherwise clearing an in-flight
  // task leaves a phantom poller (setTimeout + AbortController) running until the
  // component unmounts — it would keep patching a task that no longer exists.
  poll.stop(id)
  if (preview.value?.id === id) closePreview()
  library.removeVideoTask(id)
}

function clearAll(): void {
  closePreview()
  poll.stopAll() // drop any in-flight pollers so cleared tasks leave no phantoms
  library.clearVideoTasks()
}

async function enableNotify(): Promise<void> {
  notifyState.value = (await requestVideoNotifyPermission()) ? 'granted' : 'denied'
}

// ---- In-page video lightbox ---------------------------------------------------
const preview = ref<VideoTaskItem | null>(null)
const previewUrl = ref('')
const previewState = ref<'loading' | 'ready' | 'expired'>('loading')
// Transient "copied" confirmation for the copy-link button (no toast/banner).
const copied = ref(false)
let copiedTimer: ReturnType<typeof setTimeout> | undefined
let previewRevoke: () => void = () => {}

async function openPreview(task: VideoTaskItem): Promise<void> {
  if (!videoTaskPlaybackAvailable(task)) return
  previewRevoke()
  preview.value = task
  previewUrl.value = ''
  previewState.value = 'loading'
  // Playback must not turn TokenKey into a media server: http(s) upstream URLs go
  // straight to the browser; an inline data:video result becomes a tab-local Blob
  // URL we revoke on close. Shared with Bake-Off via videoPlaybackUrl.
  const playback = videoPlaybackUrl(task.url)
  previewRevoke = playback.revoke
  if (preview.value?.id !== task.id) {
    playback.revoke()
    return
  }
  previewUrl.value = playback.url
  previewState.value = playback.url ? 'ready' : 'expired'
}

// The <video> failed to load — mark the card prompt-only and close the lightbox
// instead of trapping the user in an "expired" modal they already saw on the list.
function onPreviewError(): void {
  const task = preview.value
  closePreview()
  if (task) library.patchVideoTask(task.id, { urlExpired: true, url: '' })
}

// Re-attempt playback — recovers from a transient media error without forcing
// the user to close, re-click, or pay to regenerate.
function retryPreview(): void {
  if (preview.value) void openPreview(preview.value)
}

// Copy the active preview URL to the clipboard. Best-effort: an insecure context
// or a denied permission is a no-op (Download remains the fallback), so the copy
// affordance never throws into the UI.
async function copyPreviewLink(): Promise<void> {
  if (!previewUrl.value) return
  try {
    await navigator.clipboard?.writeText(previewUrl.value)
    copied.value = true
    if (copiedTimer) clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => (copied.value = false), 1500)
  } catch {
    /* clipboard unavailable / blocked — silent, Download still works */
  }
}

function closePreview(): void {
  previewRevoke()
  previewRevoke = () => {}
  preview.value = null
  previewUrl.value = ''
  copied.value = false
}

function reuseAndClose(task: VideoTaskItem | null): void {
  if (task) reuse(task)
  closePreview()
}

function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape' && preview.value) closePreview()
}

async function generate(): Promise<void> {
  const text = prompt.value.trim()
  const resolved = selected.value
  if (!text || !props.apiKey || !resolved || sending.value || !canAfford.value || props.keyId == null) return
  errorMessage.value = ''
  errorCode.value = ''
  sending.value = true
  try {
    const raw = await gatewayVideoSubmit(props.apiKey, props.gatewayBase, {
      model: resolved.servedId,
      prompt: text,
      duration: duration.value,
      aspectRatio: aspectId.value || undefined,
      ...(supports('negativePrompt') && negativePrompt.value.trim()
        ? { negativePrompt: negativePrompt.value.trim() }
        : {}),
      ...(supports('seed') && seed.value != null ? { seed: seed.value } : {}),
      ...(supports('firstFrameImage') && firstFrameImage.value.trim()
        ? { image: firstFrameImage.value.trim() }
        : {}),
    })
    const taskId = extractVideoTaskId(raw)
    if (!taskId) throw new Error(t('studio.video.noTaskId'))
    const state = videoStateFromFetch(raw)
    const task: VideoTaskItem = {
      id: taskId,
      prompt: text,
      model: resolved.servedId,
      vendorLabel: resolved.model.vendorLabel,
      seconds: duration.value,
      aspectRatio: aspectId.value || undefined,
      estCost: estimate.value,
      keyId: props.keyId,
      state,
      url: state === 'succeeded' ? extractVideoUrl(raw) : '',
      submittedAtMs: Date.now(),
      elapsedS: 0,
    }
    library.upsertVideoTask(task)
    emit('spent') // balance reserved by the pre-flight hold
    if (state === 'processing') poll.resume(task)
  } catch (e) {
    const msg = e instanceof Error ? e.message : t('studio.errors.generic')
    const code = classifyGatewayError(msg)
    errorCode.value = code
    errorMessage.value = code === 'generic' ? msg : t(studioErrorI18nKey(code))
  } finally {
    sending.value = false
  }
}

onMounted(() => {
  if (typeof window !== 'undefined' && 'Notification' in window) {
    notifyState.value =
      Notification.permission === 'granted' ? 'granted' : Notification.permission === 'denied' ? 'denied' : 'idle'
  }
  window.addEventListener('keydown', onKeydown)
  // Reattach polling for any in-flight task persisted across a reload. Succeeded
  // tasks are browser-local previews: upstream URLs are used directly and inline
  // data:video payloads are intentionally not persisted.
  for (const task of library.videoTasks.value) {
    if (task.state === 'processing') poll.resume(task)
  }
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  if (copiedTimer) clearTimeout(copiedTimer)
  previewRevoke()
})
</script>

<style scoped>
/* Indeterminate progress sweep — communicates "working" without a fake percentage. */
@keyframes tk-video-indeterminate {
  0% {
    transform: translateX(-110%);
  }
  100% {
    transform: translateX(360%);
  }
}
.tk-indeterminate {
  animation: tk-video-indeterminate 1.4s ease-in-out infinite;
}
@media (prefers-reduced-motion: reduce) {
  .tk-indeterminate {
    animation: none;
  }
}
</style>
