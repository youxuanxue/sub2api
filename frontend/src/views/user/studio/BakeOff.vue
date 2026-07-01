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
          :disabled="isBusy"
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
            :disabled="isBusy || (!selectedModelIds.includes(r.model.modelId) && selectedModelIds.length >= MAX_PANELS)"
            data-testid="bakeoff-tier"
            @click="toggleModel(r.model.modelId)"
          >
            {{ r.model.displayName }}
            <span class="opacity-70">{{ modality === 'image' ? formatUsd(r.baseImagePrice || 0) + t('studio.image.perImageUnit') : formatUsd(r.perSecond || 0) + t('studio.video.perSecondUnit') }}</span>
          </button>
          <p
            v-if="selectedModelIds.length >= MAX_PANELS && models.length > MAX_PANELS"
            class="w-full text-[11px] text-amber-700 dark:text-amber-300"
            data-testid="bakeoff-max-models-hint"
          >
            {{ t('studio.bakeoff.maxModelsHint', { max: MAX_PANELS }) }}
          </p>
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
            :data-testid="`bakeoff-duration-${d}`"
            :disabled="isBusy"
            @click="duration = d"
          >
            {{ d }} s
          </button>
        </div>
        <div
          v-if="modality === 'video' && videoModelsSupportGenerateAudio"
          class="mt-3 flex items-center justify-between gap-3 rounded-lg border border-gray-100 bg-gray-50 px-3 py-2 dark:border-dark-700 dark:bg-dark-950/60"
        >
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
            data-testid="bakeoff-video-generate-audio"
            :disabled="isBusy"
            @click="generateAudio = !generateAudio"
          >
            <span
              class="absolute top-0.5 h-6 w-6 rounded-full bg-white shadow transition"
              :class="generateAudio ? 'left-[22px]' : 'left-0.5'"
            />
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
            {{ runButtonLabel }}
          </button>
          <button
            v-if="panels.length && !isBusy"
            type="button"
            class="rounded-xl border border-gray-200 px-4 py-2.5 text-sm font-medium text-gray-600 transition hover:border-gray-300 dark:border-dark-600 dark:text-dark-300"
            data-testid="studio-bakeoff-clear-results"
            @click="clearResults"
          >
            {{ t('studio.bakeoff.clearResults') }}
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

    <StudioLocalSaveBanner v-if="showSaveReminder" test-id="studio-bakeoff-save-reminder" />

    <!-- side-by-side panels -->
    <div v-if="panels.length" class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-[repeat(auto-fit,minmax(220px,1fr))]">
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
          <StudioImageExpired v-else-if="p.state === 'succeeded'" compact />
          <div v-else class="flex aspect-square items-center justify-center rounded-lg bg-gray-50 text-xs text-gray-400 dark:bg-dark-800 dark:text-dark-500">
            <span v-if="p.state === 'processing'">{{ t('studio.bakeoff.generating') }}</span>
            <span v-else-if="p.state === 'failed'" class="px-2 text-center text-red-500">{{ p.errorMessage || t('studio.bakeoff.failed') }}</span>
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
            v-if="bakePanelPresentation(p) === 'inline-play'"
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
          <StudioVideoPreviewChecking
            v-else-if="bakePanelPresentation(p) === 'loading'"
            aspect-ratio="16 / 9"
            test-id="bakeoff-video-checking"
          />
          <StudioVideoDownloadCard
            v-else-if="bakePanelPresentation(p) === 'download-only' && panelPlaybackTask(p)"
            :prompt="prompt"
            :task="panelPlaybackTask(p)!"
            :copied="copiedUrl === p.url"
            test-id="bakeoff-video-download-only"
            download-test-id="bakeoff-video-download-primary"
            copy-test-id="bakeoff-video-copy-primary"
            @download="downloadCardVideo(p.url!, `tokenkey-${p.taskId || p.modelId}.mp4`, p.urlExpired)"
            @copy-link="copyCardLink(p.url!, `tokenkey-${p.taskId || p.modelId}.mp4`)"
          />
          <StudioVideoUnavailable
            v-else-if="p.state === 'succeeded'"
            :prompt="prompt"
            :task="panelPlaybackTask(p)"
            test-id="bakeoff-video-expired"
          />
          <div v-else class="flex aspect-video items-center justify-center rounded-lg bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-dark-400">
            <span v-if="p.state === 'processing'" class="inline-flex items-center gap-1.5"><span class="h-2 w-2 animate-pulse rounded-full bg-primary-500"></span>{{ t('studio.video.statusProcessing') }} {{ formatElapsed(p.elapsedS || 0) }}</span>
            <span v-else-if="p.state === 'failed'" class="px-2 text-center text-red-500">{{ p.errorMessage || t('studio.bakeoff.failed') }}</span>
            <span v-else>—</span>
          </div>
        </template>
        <div class="mt-2 flex flex-wrap items-center justify-between gap-2 text-sm">
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-bold text-primary-700 dark:text-primary-300">{{ formatUsd(p.cost) }}</span>
            <StudioPlaybackBadge
              v-if="modality === 'video' && p.state === 'succeeded' && panelPlaybackTask(p) && bakePanelPresentation(p) === 'loading'"
              :task="panelPlaybackTask(p)!"
            />
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <button
              v-if="modality === 'image' && p.src"
              type="button"
              class="text-[11px] font-medium text-primary-600 dark:text-primary-300"
              @click="downloadImage(p.src, p.modelId)"
            >
              {{ t('studio.image.download') }}
            </button>
            <template v-else-if="modality === 'video' && p.state === 'succeeded' && p.url && bakePanelPresentation(p) !== 'download-only'">
              <button
                type="button"
                class="text-[11px] font-medium text-primary-600 dark:text-primary-300"
                data-testid="bakeoff-video-download"
                @click="downloadCardVideo(p.url, `tokenkey-${p.taskId || p.modelId}.mp4`, p.urlExpired)"
              >
                {{ t('studio.video.download') }}
              </button>
              <button
                type="button"
                class="text-[11px] font-medium text-gray-500 dark:text-dark-300"
                data-testid="bakeoff-video-copy-card-link"
                @click="copyCardLink(p.url, `tokenkey-${p.taskId || p.modelId}.mp4`)"
              >
                {{ copiedUrl === p.url ? t('studio.video.copied') : t('studio.video.copyLink') }}
              </button>
            </template>
            <span v-if="p.elapsedS != null && p.state !== 'idle'" class="text-xs text-gray-500 dark:text-dark-400">⏱ {{ p.elapsedS }}s</span>
          </div>
        </div>
      </div>
    </div>

    <div
      v-if="modality === 'image' ? imageHistoryRuns.length : videoHistoryRuns.length"
      class="space-y-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900"
      data-testid="bakeoff-history"
    >
      <div class="flex items-center justify-between">
        <span class="text-sm font-semibold text-gray-700 dark:text-dark-200">{{ t('studio.bakeoff.historyTitle') }}</span>
      </div>
      <template v-if="modality === 'image'">
        <div v-for="run in imageHistoryRuns" :key="run.key" class="space-y-2 border-t border-gray-100 pt-4 first:border-t-0 first:pt-0 dark:border-dark-800">
          <p class="truncate text-xs text-gray-500 dark:text-dark-400" :title="run.prompt">{{ run.prompt }}</p>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-[repeat(auto-fit,minmax(220px,1fr))]">
            <div
              v-for="img in run.items"
              :key="img.id"
              class="rounded-xl border border-gray-200 p-2 dark:border-dark-700"
              data-testid="bakeoff-history-item"
            >
              <div class="mb-1 flex items-center justify-between gap-2">
                <span class="truncate text-xs font-semibold text-gray-800 dark:text-dark-200">{{ modelLabel(img.model) }}</span>
                <span class="shrink-0 text-[10px] text-gray-400 dark:text-dark-500">{{ t('studio.via', { vendor: img.vendorLabel }) }}</span>
              </div>
              <div v-if="imageHistoryItemAvailable(img)" class="overflow-hidden rounded-lg">
                <img
                  :src="img.src"
                  :alt="img.prompt"
                  class="aspect-square w-full object-cover"
                  loading="lazy"
                  @error="onImageHistoryThumbError(img.id)"
                />
              </div>
              <StudioImageExpired v-else compact />
              <div class="mt-1 flex items-center justify-between gap-2">
                <span class="text-xs font-bold text-primary-700 dark:text-primary-300">{{ formatUsd(img.cost) }}</span>
                <button
                  v-if="imageHistoryItemAvailable(img)"
                  type="button"
                  class="text-[10px] font-medium text-primary-600 dark:text-primary-300"
                  @click="downloadImage(img.src, img.id)"
                >
                  {{ t('studio.image.download') }}
                </button>
              </div>
            </div>
          </div>
        </div>
      </template>
      <template v-else>
        <div v-for="run in videoHistoryRuns" :key="run.key" class="space-y-2 border-t border-gray-100 pt-4 first:border-t-0 first:pt-0 dark:border-dark-800">
          <p class="truncate text-xs text-gray-500 dark:text-dark-400" :title="run.prompt">{{ run.prompt }}</p>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-[repeat(auto-fit,minmax(220px,1fr))]">
            <div
              v-for="task in run.items"
              :key="task.id"
              class="rounded-xl border border-gray-200 p-2 dark:border-dark-700"
              data-testid="bakeoff-history-item"
            >
              <div class="mb-1 flex items-center justify-between gap-2">
                <span class="truncate text-xs font-semibold text-gray-800 dark:text-dark-200">{{ modelLabel(task.model) }}</span>
                <span class="shrink-0 text-[10px] text-gray-400 dark:text-dark-500">{{ t('studio.via', { vendor: task.vendorLabel }) }}</span>
              </div>
              <button
                v-if="videoTaskCardPresentation(task) === 'inline-play'"
                type="button"
                class="group relative block aspect-video w-full overflow-hidden rounded-lg bg-gradient-to-br from-gray-800 to-gray-950"
                data-testid="bakeoff-history-video-play"
                @click="openVideoHistoryPreview(task)"
              >
                <span class="pointer-events-none absolute inset-0 flex items-center justify-center">
                  <span class="flex h-10 w-10 items-center justify-center rounded-full bg-white/90 shadow-lg transition group-hover:scale-110">
                    <span aria-hidden="true" class="ml-0.5 text-lg text-gray-900">▶</span>
                  </span>
                </span>
              </button>
              <StudioVideoPreviewChecking
                v-else-if="videoTaskCardPresentation(task) === 'loading'"
                aspect-ratio="16 / 9"
                test-id="bakeoff-history-video-checking"
              />
              <StudioVideoDownloadCard
                v-else-if="videoTaskCardPresentation(task) === 'download-only'"
                :prompt="task.prompt"
                :task="task"
                :copied="copiedUrl === task.url"
                test-id="bakeoff-history-video-download-only"
                download-test-id="bakeoff-history-video-download-primary"
                copy-test-id="bakeoff-history-video-copy-primary"
                @download="downloadCardVideo(task.url, `tokenkey-${task.id}.mp4`, task.urlExpired)"
                @copy-link="copyCardLink(task.url, `tokenkey-${task.id}.mp4`)"
              />
              <div v-else-if="task.state === 'processing'" class="flex aspect-video items-center justify-center rounded-lg bg-gray-50 text-[11px] text-gray-500 dark:bg-dark-800 dark:text-dark-400">
                <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 animate-pulse rounded-full bg-primary-500"></span>{{ t('studio.video.statusProcessing') }}</span>
              </div>
              <div v-else-if="task.state === 'failed'" class="flex aspect-video items-center justify-center rounded-lg bg-gray-50 text-[11px] text-red-500 dark:bg-dark-800">
                {{ t('studio.bakeoff.failed') }}
              </div>
              <StudioVideoUnavailable v-else :prompt="task.prompt" :task="task" />
              <div class="mt-1 flex items-center justify-between gap-2">
                <StudioPlaybackBadge
                  v-if="task.state === 'succeeded' && videoTaskCardPresentation(task) === 'loading'"
                  :task="task"
                />
                <span class="ml-auto text-xs font-bold text-primary-700 dark:text-primary-300">{{ formatUsd(task.estCost) }}</span>
              </div>
              <div
                v-if="task.state === 'succeeded' && task.url && videoTaskCardPresentation(task) !== 'download-only'"
                class="mt-1 flex flex-wrap items-center gap-2"
              >
                <button
                  type="button"
                  class="text-[10px] font-medium text-primary-600 dark:text-primary-300"
                  data-testid="bakeoff-history-video-download"
                  @click="downloadCardVideo(task.url, `tokenkey-${task.id}.mp4`, task.urlExpired)"
                >
                  {{ t('studio.video.download') }}
                </button>
                <button
                  type="button"
                  class="text-[10px] font-medium text-gray-500 dark:text-dark-300"
                  data-testid="bakeoff-history-video-copy-link"
                  @click="copyCardLink(task.url, `tokenkey-${task.id}.mp4`)"
                >
                  {{ copiedUrl === task.url ? t('studio.video.copied') : t('studio.video.copyLink') }}
                </button>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>

    <StudioVideoPreviewLightbox
      :open="previewOpen"
      :preview-state="previewState"
      :preview-url="previewUrl"
      :download-url="previewDownloadUrl"
      :download-filename="previewDownloadFilename"
      :label="previewLabel"
      :cost="previewCost"
      :preview-media-ready="previewMediaReady"
      :copied-link="previewCopiedLink"
      test-id="bakeoff-video-preview"
      close-test-id="bakeoff-video-preview-close"
      copy-link-test-id="bakeoff-video-copy-link"
      @close="closePreviewLightbox"
      @error="onPreviewError"
      @retry="retryPreviewLightbox"
      @copy-link="copyPreviewLink"
      @download="downloadPreview"
      @media-ready="onPreviewMediaReady"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
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
import {
  groupImageHistoryByTs,
  groupVideoHistoryByBatch,
  imageHistoryItemAvailable,
  shouldShowStudioSaveReminder,
  videoTaskCardPresentation,
  videoTaskPlaybackAvailable,
} from '@/utils/studioMedia.tk'
import { downloadMedia } from '@/utils/studioDownload.tk'
import { tagStudioVideoPlayback } from '@/utils/studioPlaybackStorage.tk'
import StudioLocalSaveBanner from '@/views/user/studio/components/StudioLocalSaveBanner.vue'
import StudioImageExpired from '@/views/user/studio/components/StudioImageExpired.vue'
import StudioPlaybackBadge from '@/views/user/studio/components/StudioPlaybackBadge.vue'
import StudioVideoDownloadCard from '@/views/user/studio/components/StudioVideoDownloadCard.vue'
import StudioVideoPreviewChecking from '@/views/user/studio/components/StudioVideoPreviewChecking.vue'
import StudioVideoPreviewLightbox from '@/views/user/studio/components/StudioVideoPreviewLightbox.vue'
import StudioVideoUnavailable from '@/views/user/studio/components/StudioVideoUnavailable.vue'
import { useAppStore } from '@/stores/app'
import { classifyGatewayError, parseGatewayErrorMessage, studioErrorI18nKey, type StudioErrorCode } from '@/utils/studioGatewayError.tk'
import { useStudioVideoCardActions } from '@/composables/useStudioVideoCardActions'
import { useStudioVideoSubmitOptions } from '@/composables/useStudioVideoSubmitOptions'
import { useStudioVideoPreview } from '@/composables/useStudioVideoPreview'
import { useVideoTaskPoll } from '@/composables/useVideoTaskPoll'
import { useMediaLibrary, type ImageHistoryItem, type VideoTaskItem } from '@/composables/useMediaLibrary'
import { studioImageHistoryId } from '@/utils/studioImageHistory.tk'
import { mountStudioImageLibrary, onStudioImageThumbError } from '@/composables/useStudioImageLibrary'
import { mountStudioVideoLibrary } from '@/composables/useStudioVideoLibrary'
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
const appStore = useAppStore()
const warnExpiredDownload = () => appStore.showWarning(t('studio.video.expiredHint'), 8000)
const warnInlineCopy = () => appStore.showWarning(t('studio.video.inlineCopyHint'), 8000)
const { copiedUrl, copyCardLink, downloadCardVideo } = useStudioVideoCardActions({
  onExpiredDownload: warnExpiredDownload,
  onInlineCopyUnsupported: warnInlineCopy,
})
const { generateAudio } = useStudioVideoSubmitOptions()

const MAX_PANELS = 6
/** Imagen / seedream: ratio code on /v1/images/generations (see ImageStudio sentSize). */
const DEFAULT_IMAGEN_SIZE = '1:1'
/** Gemini-native image: aspect_ratio via /v1/chat/completions extra_body.google.image_config. */
const DEFAULT_GEMINI_ASPECT = '1:1'

const modality = ref<StudioModality>('video')
const models = computed(() => resolveAvailableModels(modality.value, props.availableIds, props.priceMap))
const videoModelsSupportGenerateAudio = computed(() =>
  models.value.some((r) => r.model.supportedParams.includes('generateAudio'))
)
const selectedModelIds = ref<string[]>([])
const prompt = ref('')
const lastRunPrompt = ref('')
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
  urlExpired?: boolean
  playbackStorage?: import('@/utils/studioPlaybackStorage.tk').StudioPlaybackStorage
  elapsedS?: number
  errorMessage?: string
}
const panels = ref<BakePanel[]>([])
/** Submit phase OR any panel still polling upstream — one gate for the whole run. */
const isBusy = computed(
  () => running.value || panels.value.some((p) => p.state === 'processing')
)
const activeRunTs = ref<number | null>(null)

const library = useMediaLibrary(props.userId)

function tagVideoPlayback(taskId: string, url: string): void {
  void tagStudioVideoPlayback(playbackDeps, taskId, url)
}

function patchVideoTask(id: string, patch: Partial<VideoTaskItem>): void {
  library.patchVideoTask(id, patch)
  const panel = panels.value.find((p) => p.taskId === id)
  if (panel) {
    if (patch.state) panel.state = patch.state
    if (typeof patch.url === 'string' && (patch.url !== '' || patch.state !== 'processing')) {
      panel.url = patch.url
    }
    if (patch.urlExpired != null) panel.urlExpired = patch.urlExpired
    if (patch.playbackStorage != null) panel.playbackStorage = patch.playbackStorage
    if (patch.elapsedS != null) panel.elapsedS = patch.elapsedS
    if (patch.errorMessage != null) panel.errorMessage = patch.errorMessage
  }
  if (patch.url) void tagVideoPlayback(id, patch.url)
}

const playbackDeps = {
  patchVideoTask,
  cacheInlineMedia: library.cacheInlineMedia.bind(library),
  onUpstreamCorsBlocked: () => appStore.showWarning(t('studio.playback.upstreamCorsBlocked'), 8000),
}

const poll = useVideoTaskPoll({
  gatewayBase: () => props.gatewayBase,
  resolveKey: (keyId) => props.keys.find((k) => k.id === keyId)?.key,
  patch: patchVideoTask,
  onTerminal: () => emit('spent'),
})

type ImageHistoryRunView = { key: string; prompt: string; items: ImageHistoryItem[] }
type VideoHistoryRunView = { key: string; prompt: string; items: VideoTaskItem[] }

const imageHistoryRuns = computed<ImageHistoryRunView[]>(() => {
  const hideTs = panels.value.length ? activeRunTs.value : null
  return groupImageHistoryByTs(library.images.value)
    .filter((run) => run.ts !== hideTs)
    .map((run) => ({ key: `img-${run.ts}`, prompt: run.prompt, items: run.items }))
})

const videoHistoryRuns = computed<VideoHistoryRunView[]>(() => {
  const hideTs = panels.value.length ? activeRunTs.value : null
  return groupVideoHistoryByBatch(library.videoTasks.value)
    .filter((run) => run.batchMs !== hideTs)
    .map((run) => ({ key: `vid-${run.batchMs}`, prompt: run.prompt, items: run.items }))
})

const showSaveReminder = computed(() =>
  shouldShowStudioSaveReminder({
    imageCount: library.images.value.length,
    videoTaskCount: library.videoTasks.value.length,
    activeResultCount: panels.value.length,
  })
)

function bakePanelPresentation(p: BakePanel) {
  if (p.state !== 'succeeded') return 'expired' as const
  const stored = p.taskId ? library.videoTasks.value.find((t) => t.id === p.taskId) : undefined
  return videoTaskCardPresentation({
    state: p.state,
    url: p.url ?? stored?.url ?? '',
    urlExpired: p.urlExpired ?? stored?.urlExpired,
    playbackStorage: p.playbackStorage ?? stored?.playbackStorage,
  })
}

function panelPlaybackTask(p: BakePanel): Pick<VideoTaskItem, 'playbackStorage' | 'urlExpired' | 'url'> | undefined {
  if (!p.url && !p.urlExpired) return undefined
  const stored = p.taskId ? library.videoTasks.value.find((t) => t.id === p.taskId) : undefined
  const url = p.url ?? stored?.url ?? ''
  if (!url && !p.urlExpired && !stored?.urlExpired) return undefined
  return {
    url,
    urlExpired: p.urlExpired ?? stored?.urlExpired,
    playbackStorage: stored?.playbackStorage,
  }
}

// ---- In-page video lightbox (shared with VideoStudio) -------------------------
const {
  open: previewOpen,
  previewUrl,
  downloadUrl: previewDownloadUrl,
  downloadFilename: previewDownloadFilename,
  label: previewLabel,
  cost: previewCost,
  previewState,
  previewMediaReady,
  copiedLink: previewCopiedLink,
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

function openVideoPreviewFromUrl(label: string, cost: number, url: string, taskId?: string, urlExpired?: boolean): void {
  openPreviewLightbox({
    url,
    label,
    cost,
    taskId,
    urlExpired,
    downloadFilename: taskId ? `tokenkey-${taskId}.mp4` : 'tokenkey-bakeoff-preview.mp4',
  })
}

function openVideoPreview(panel: BakePanel): void {
  if (panel.state !== 'succeeded' || !panel.url) return
  if (!videoTaskPlaybackAvailable({ state: panel.state, url: panel.url ?? '', urlExpired: panel.urlExpired, playbackStorage: panel.playbackStorage })) return
  openVideoPreviewFromUrl(panel.label, panel.cost, panel.url, panel.taskId, panel.urlExpired)
}

function openVideoHistoryPreview(task: VideoTaskItem): void {
  if (!videoTaskPlaybackAvailable(task) || !task.url) return
  openVideoPreviewFromUrl(modelLabel(task.model), task.estCost, task.url, task.id, task.urlExpired)
}

function downloadImage(src: string, id: string): void {
  downloadMedia(src, `tokenkey-${id}.png`)
}

function modelLabel(modelId: string): string {
  const hit = models.value.find((r) => r.model.modelId === modelId || r.servedId === modelId)
  return hit?.model.displayName ?? modelId
}

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

// Re-sync whenever the selected-model union changes so the default does not
// depend on click order (Veo-first kept 8s while Seedance-first jumped to 10s).
watch(durationOptions, (opts) => {
  if (opts.length) duration.value = videoDurationDefault(opts)
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
  () => !isBusy.value && !!props.apiKey && !!prompt.value.trim() && selectedModelIds.value.length >= 2 && canAfford.value && props.keyId != null
)
const promptChangedSinceRun = computed(
  () => panels.value.length > 0 && prompt.value.trim() !== lastRunPrompt.value
)
const runButtonLabel = computed(() => {
  if (isBusy.value) return t('studio.bakeoff.running')
  if (panels.value.length && !promptChangedSinceRun.value) return t('studio.bakeoff.regenerate')
  if (panels.value.length && promptChangedSinceRun.value) return t('studio.bakeoff.regenerateChanged')
  return t('studio.bakeoff.run', { count: selectedModelIds.value.length })
})

function setModality(m: StudioModality): void {
  if (isBusy.value) return
  modality.value = m
  selectedModelIds.value = []
  duration.value = VIDEO_DURATION_DEFAULT
  panels.value = []
  lastRunPrompt.value = ''
  activeRunTs.value = null
}

function clearResults(): void {
  if (isBusy.value) return
  panels.value = []
  lastRunPrompt.value = ''
  activeRunTs.value = null
  errorMessage.value = ''
  errorCode.value = ''
}

function toggleModel(id: string): void {
  if (isBusy.value) return
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
  if (!text || chosen.length < 2 || isBusy.value || !canAfford.value || props.keyId == null) return
  errorMessage.value = ''
  errorCode.value = ''
  running.value = true
  lastRunPrompt.value = text
  const runTs = Date.now()
  activeRunTs.value = runTs
  poll.stopAll()
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
      const historyBatch: ImageHistoryItem[] = []
      await Promise.all(
        panels.value.map(async (panel) => {
          try {
            const items = await generateBakeoffImage(panel.servedId, text)
            if (!items.length) throw new Error('no_image')
            const it = items[0]
            panel.src = it.src
            panel.state = 'succeeded'
            historyBatch.push({
              id: studioImageHistoryId(),
              src: it.src,
              s3Key: it.s3Key,
              prompt: text,
              revisedPrompt: it.revisedPrompt,
              model: panel.servedId,
              vendorLabel: panel.vendorLabel,
              size: DEFAULT_IMAGEN_SIZE,
              cost: panel.cost,
              ts: runTs,
            })
          } catch (e) {
            panel.state = 'failed'
            panel.errorMessage = panelErrorText(e)
            mapError(e)
          }
        })
      )
      if (historyBatch.length) library.addImages(historyBatch)
      emit('spent')
    } else {
      // Submit all videos, then poll each.
      await Promise.all(
        panels.value.map(async (panel) => {
          try {
            const raw = await gatewayVideoSubmit(props.apiKey, props.gatewayBase, {
              model: panel.servedId,
              prompt: text,
              duration: panel.seconds,
              ...(videoModelsSupportGenerateAudio.value ? { generateAudio: generateAudio.value } : {}),
            })
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
              submittedAtMs: runTs,
              elapsedS: 0,
            }
            library.upsertVideoTask(task)
            if (state === 'processing') poll.resume(task)
            else if (panel.url) void tagVideoPlayback(taskId, panel.url)
          } catch (e) {
            panel.state = 'failed'
            panel.errorMessage = panelErrorText(e)
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

function panelErrorText(e: unknown): string {
  const msg = parseGatewayErrorMessage(e instanceof Error ? e.message : '')
  const code = classifyGatewayError(msg)
  if (code !== 'generic') return t(studioErrorI18nKey(code))
  if (msg && msg !== 'no_image' && msg !== 'no_task') return msg
  return t('studio.bakeoff.panelError')
}

function mapError(e: unknown): void {
  const msg = parseGatewayErrorMessage(e instanceof Error ? e.message : '')
  const code = classifyGatewayError(msg)
  if (code !== 'generic') {
    errorCode.value = code
    errorMessage.value = t(studioErrorI18nKey(code))
  } else if (!errorMessage.value && msg && msg !== 'no_image' && msg !== 'no_task') {
    errorMessage.value = msg
  }
}

function onImageHistoryThumbError(itemId: string): void {
  void onStudioImageThumbError(library, itemId)
}

onMounted(() => {
  for (const task of library.videoTasks.value) {
    if (task.state === 'processing') poll.resume(task)
  }
  void mountStudioImageLibrary(props.apiKey, props.gatewayBase, library)
  void mountStudioVideoLibrary(library)
})
</script>
