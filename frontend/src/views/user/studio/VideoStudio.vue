<template>
  <div class="grid grid-cols-1 gap-5 lg:grid-cols-[360px_1fr]">
    <!-- LEFT: orchestration -->
    <div class="space-y-4">
      <div v-if="models.length === 0" class="rounded-xl border border-dashed border-gray-300 bg-white/60 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400" data-testid="studio-video-model-empty">
        <p>{{ t('studio.video.modelEmpty') }}</p>
        <p v-if="anyKeyServesVideo" class="mt-2 font-medium text-amber-800 dark:text-amber-200">{{ t('studio.video.modelEmptySwitchKey') }}</p>
        <p v-else class="mt-2 text-xs text-gray-400 dark:text-dark-500">{{ t('studio.video.modelEmptyAllKeys') }}</p>
        <router-link class="mt-2 block font-medium text-primary-600 underline dark:text-primary-400" to="/pricing">
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
        <div v-if="supports('generateAudio')" class="mt-3 flex items-center justify-between gap-3 rounded-lg border border-gray-100 bg-gray-50 px-3 py-2 dark:border-dark-700 dark:bg-dark-950/60">
          <div>
            <div class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('studio.video.generateAudio') }}</div>
            <p class="text-[11px] text-gray-500 dark:text-dark-400">{{ t('studio.video.generateAudioHint') }}</p>
          </div>
          <button
            type="button"
            role="switch"
            :aria-checked="generateAudio"
            class="relative h-7 w-12 shrink-0 rounded-full transition"
            :class="generateAudio ? 'bg-primary-600' : 'bg-gray-300 dark:bg-dark-600'"
            data-testid="studio-video-generate-audio"
            :disabled="sending"
            @click="generateAudio = !generateAudio"
          >
            <span
              class="absolute top-0.5 h-6 w-6 rounded-full bg-white shadow transition"
              :class="generateAudio ? 'left-[22px]' : 'left-0.5'"
            />
          </button>
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
      <StudioLocalSaveBanner v-if="library.videoTasks.value.length" test-id="studio-video-save-reminder" class="mb-2" />
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

        <!-- succeeded: play tile only when inline preview is eligible; CORS-blocked upstream → download-first. -->
        <div v-else-if="task.state === 'succeeded'" class="space-y-2">
          <template v-if="videoTaskCardPresentation(task) === 'inline-play'">
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
            <div class="mt-2">
              <StudioPlaybackBadge :task="task" />
            </div>
          </template>
          <StudioVideoPreviewChecking
            v-else-if="videoTaskCardPresentation(task) === 'loading'"
            :aspect-ratio="posterAspect(task)"
            :seconds="task.seconds"
            :aspect-label="task.aspectRatio"
          />
          <StudioVideoDownloadCard
            v-else-if="videoTaskCardPresentation(task) === 'download-only'"
            :prompt="task.prompt"
            :task="task"
            :seconds="task.seconds"
            :aspect-label="task.aspectRatio"
            :copied="copiedUrl === task.url"
            @download="downloadCardVideo(task.url, `tokenkey-${task.id}.mp4`, task.urlExpired)"
            @copy-link="copyCardLink(task.url, `tokenkey-${task.id}.mp4`)"
          />
          <StudioVideoUnavailable v-else :prompt="task.prompt" :task="task" />
          <div class="flex items-center justify-between text-[11px] text-gray-500 dark:text-dark-400">
            <span v-if="videoTaskCardPresentation(task) === 'inline-play'" class="truncate" :title="task.prompt">{{ task.prompt }}</span>
            <span v-else-if="videoTaskCardPresentation(task) === 'loading'" class="truncate" :title="task.prompt">{{ task.prompt }}</span>
            <span class="shrink-0 rounded bg-primary-50 px-1.5 py-0.5 font-semibold text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">{{ formatUsd(task.estCost) }}</span>
          </div>
          <div class="flex gap-3 text-[11px] font-medium text-gray-500 dark:text-dark-400">
            <button v-if="videoTaskCardPresentation(task) === 'inline-play'" type="button" class="text-primary-600 dark:text-primary-300" @click="openPreview(task)">{{ t('studio.video.play') }}</button>
            <button
              v-if="task.url && videoTaskCardPresentation(task) !== 'download-only'"
              type="button"
              class="text-primary-600 dark:text-primary-300"
              data-testid="studio-video-download"
              @click="downloadCardVideo(task.url, `tokenkey-${task.id}.mp4`, task.urlExpired)"
            >{{ t('studio.video.download') }}</button>
            <button
              v-if="task.url && videoTaskCardPresentation(task) !== 'download-only'"
              type="button"
              class="text-gray-500 dark:text-dark-300"
              data-testid="studio-video-copy-card-link"
              @click="copyCardLink(task.url, `tokenkey-${task.id}.mp4`)"
            >{{ copiedUrl === task.url ? t('studio.video.copied') : t('studio.video.copyLink') }}</button>
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

    <StudioVideoPreviewLightbox
      :open="previewOpen"
      :preview-state="previewState"
      :preview-url="previewUrl"
      :download-url="previewDownloadUrl"
      :download-filename="previewDownloadFilename"
      :label="previewLabel"
      :subtitle="previewSubtitle"
      :cost="previewCost"
      :preview-media-ready="previewMediaReady"
      :copied-link="previewCopiedLink"
      show-reuse-prompt
      @close="closePreviewLightbox"
      @error="onPreviewError"
      @retry="retryPreviewLightbox"
      @reuse="reuseAndClosePreview"
      @copy-link="copyPreviewLink"
      @download="downloadPreview"
      @media-ready="onPreviewMediaReady"
    />
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
import { videoTaskCardPresentation, videoTaskPlaybackAvailable } from '@/utils/studioMedia.tk'
import { tagStudioVideoPlayback } from '@/utils/studioPlaybackStorage.tk'
import StudioLocalSaveBanner from '@/views/user/studio/components/StudioLocalSaveBanner.vue'
import StudioPlaybackBadge from '@/views/user/studio/components/StudioPlaybackBadge.vue'
import StudioVideoDownloadCard from '@/views/user/studio/components/StudioVideoDownloadCard.vue'
import StudioVideoPreviewChecking from '@/views/user/studio/components/StudioVideoPreviewChecking.vue'
import StudioVideoPreviewLightbox from '@/views/user/studio/components/StudioVideoPreviewLightbox.vue'
import StudioVideoUnavailable from '@/views/user/studio/components/StudioVideoUnavailable.vue'
import { classifyGatewayError, studioErrorI18nKey, type StudioErrorCode } from '@/utils/studioGatewayError.tk'
import { useMediaLibrary, type VideoTaskItem } from '@/composables/useMediaLibrary'
import { useStudioVideoCardActions } from '@/composables/useStudioVideoCardActions'
import { mountStudioVideoLibrary } from '@/composables/useStudioVideoLibrary'
import { useStudioVideoSubmitOptions } from '@/composables/useStudioVideoSubmitOptions'
import { useStudioVideoPreview } from '@/composables/useStudioVideoPreview'
import { useVideoTaskPoll, requestVideoNotifyPermission, maybeNotify } from '@/composables/useVideoTaskPoll'
import { useAppStore } from '@/stores/app'
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
  /** True when another key in the dropdown serves video (empty-state hint). */
  anyKeyServesVideo?: boolean
}>()
const emit = defineEmits<{ (e: 'spent'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const warnExpiredDownload = () => appStore.showWarning(t('studio.video.expiredHint'), 8000)
const warnInlineCopy = () => appStore.showWarning(t('studio.video.inlineCopyHint'), 8000)
const { copiedUrl, copyCardLink, downloadCardVideo } = useStudioVideoCardActions({
  onExpiredDownload: warnExpiredDownload,
  onInlineCopyUnsupported: warnInlineCopy,
})
const { generateAudio } = useStudioVideoSubmitOptions()
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

const playbackDeps = {
  patchVideoTask: (id: string, patch: Partial<VideoTaskItem>) => library.patchVideoTask(id, patch),
  cacheInlineMedia: library.cacheInlineMedia.bind(library),
  onUpstreamCorsBlocked: () => appStore.showWarning(t('studio.playback.upstreamCorsBlocked'), 8000),
}

function tagVideoPlayback(taskId: string, url: string): void {
  void tagStudioVideoPlayback(playbackDeps, taskId, url)
}

const poll = useVideoTaskPoll({
  gatewayBase: () => props.gatewayBase,
  resolveKey: (keyId) => props.keys.find((k) => k.id === keyId)?.key,
  patch: (id, patch) => {
    library.patchVideoTask(id, patch)
    if (patch.url) void tagVideoPlayback(id, patch.url)
  },
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
  poll.stop(id)
  if (previewTaskId.value === id) closePreviewLightbox()
  library.removeVideoTask(id)
}

function clearAll(): void {
  closePreviewLightbox()
  poll.stopAll()
  library.clearVideoTasks()
}

async function enableNotify(): Promise<void> {
  notifyState.value = (await requestVideoNotifyPermission()) ? 'granted' : 'denied'
}

const previewTask = ref<VideoTaskItem | null>(null)
const {
  open: previewOpen,
  previewUrl,
  downloadUrl: previewDownloadUrl,
  downloadFilename: previewDownloadFilename,
  label: previewLabel,
  subtitle: previewSubtitle,
  cost: previewCost,
  previewState,
  previewMediaReady,
  copiedLink: previewCopiedLink,
  taskId: previewTaskId,
  openPreview: openPreviewLightbox,
  closePreview: closePreviewLightbox,
  onPreviewError,
  retryPreview: retryPreviewLightbox,
  onPreviewMediaReady,
  copyPreviewLink,
  downloadPreview,
} = useStudioVideoPreview({
  onExpiredDownload: warnExpiredDownload,
  onInlineCopyUnsupported: warnInlineCopy,
})

function openPreview(task: VideoTaskItem): void {
  if (!videoTaskPlaybackAvailable(task)) return
  previewTask.value = task
  openPreviewLightbox({
    url: task.url,
    label: task.model,
    subtitle: task.prompt,
    cost: task.estCost,
    taskId: task.id,
    urlExpired: task.urlExpired,
    downloadFilename: `tokenkey-${task.id}.mp4`,
  })
}

function reuseAndClosePreview(): void {
  if (previewTask.value) reuse(previewTask.value)
  closePreviewLightbox()
  previewTask.value = null
}

function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape' && previewOpen.value) closePreviewLightbox()
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
      ...(supports('generateAudio') ? { generateAudio: generateAudio.value } : {}),
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
    if (task.url) void tagVideoPlayback(task.id, task.url)
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
  void mountStudioVideoLibrary(library)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
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
