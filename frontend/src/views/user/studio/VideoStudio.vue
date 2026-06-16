<template>
  <div class="grid grid-cols-1 gap-5 lg:grid-cols-[340px_1fr_250px]">
    <!-- LEFT: orchestration -->
    <div class="space-y-4">
      <div v-if="tiers.length === 0" class="rounded-xl border border-dashed border-gray-300 bg-white/60 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400">
        {{ t('studio.video.tierEmpty') }}
        <router-link class="mt-1 block font-medium text-primary-600 underline dark:text-primary-400" to="/pricing">
          {{ t('studio.viewPricing') }}
        </router-link>
      </div>

      <div v-else class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <div class="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ t('studio.video.tierLabel') }}</div>
        <div class="grid grid-cols-3 gap-2">
          <button
            v-for="r in tiers"
            :key="r.tier.id"
            type="button"
            class="rounded-xl border p-2.5 text-left transition"
            :class="selectedTierId === r.tier.id
              ? 'border-primary-500 bg-primary-50 ring-2 ring-primary-500/30 dark:border-primary-500 dark:bg-primary-950/40'
              : 'border-gray-200 hover:border-primary-300 dark:border-dark-600'"
            @click="selectTier(r.tier.id)"
          >
            <div class="text-[13px] font-semibold text-gray-900 dark:text-white">{{ t(r.tier.labelKey) }}</div>
            <div class="text-[11px] text-gray-500 dark:text-dark-400">{{ t(r.tier.taglineKey) }}</div>
            <div class="mt-1 text-[12px] font-bold text-primary-700 dark:text-primary-300">
              {{ formatUsd(r.candidate.perSecond || 0) }}{{ t('studio.video.perSecondUnit') }}
            </div>
            <div class="text-[10px] text-gray-400 dark:text-dark-500">{{ t('studio.via', { vendor: r.candidate.vendorLabel }) }}</div>
          </button>
        </div>
      </div>

      <div v-if="tiers.length" class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <textarea
          v-model="prompt"
          rows="3"
          class="w-full resize-none rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
          :placeholder="t('studio.video.promptPlaceholder')"
          :disabled="sending"
          @input="userEditedPrompt = true"
        />
        <div class="mt-3">
          <div class="mb-2 flex items-center justify-between">
            <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('studio.video.duration') }}</span>
            <span class="rounded-md bg-primary-50 px-2 py-0.5 text-sm font-bold text-primary-700 tabular-nums dark:bg-primary-950/50 dark:text-primary-300">{{ duration }} s</span>
          </div>
          <input
            v-model.number="duration"
            type="range"
            :min="VIDEO_DURATION_MIN"
            :max="VIDEO_DURATION_MAX"
            step="1"
            class="w-full accent-primary-600"
            :disabled="sending"
          />
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
      </div>
    </div>

    <!-- CENTER: task stack -->
    <div class="space-y-3">
      <div
        v-if="lastEvent"
        class="flex items-center justify-between gap-2 rounded-xl border border-primary-200 bg-primary-50 px-3 py-2 text-sm text-primary-800 dark:border-primary-900/50 dark:bg-primary-950/40 dark:text-primary-200"
      >
        <span>{{ lastEvent }}</span>
        <button type="button" class="text-xs text-primary-600 dark:text-primary-300" @click="lastEvent = ''">✕</button>
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
            <div class="flex items-center gap-2 text-gray-700 dark:text-dark-200"><span class="text-primary-600">⬤</span> {{ t('studio.video.stepSubmitted') }} <span class="text-green-600">✓</span></div>
            <div class="flex items-center gap-2 text-gray-700 dark:text-dark-200"><span class="text-primary-600">◔</span> {{ t('studio.video.stepGenerating') }} <span class="ml-auto font-mono text-gray-400 tabular-nums dark:text-dark-500">{{ formatElapsed(task.elapsedS) }}</span></div>
            <div class="flex items-center gap-2 text-gray-400 dark:text-dark-500"><span>○</span> {{ t('studio.video.stepReady') }}</div>
          </div>
          <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-100 dark:bg-dark-800">
            <div class="h-full animate-pulse rounded-full bg-gradient-to-r from-primary-400 to-primary-600" style="width: 62%"></div>
          </div>
          <div class="flex items-center justify-between text-[11px]">
            <span class="text-gray-500 dark:text-dark-400">{{ t('studio.video.reserved', { cost: formatUsd(task.estCost) }) }}</span>
            <button type="button" class="font-medium text-primary-600 dark:text-primary-300" @click="enableNotify">{{ t('studio.video.notifyMe') }}</button>
          </div>
          <p class="text-[11px] text-gray-400 dark:text-dark-500">{{ t('studio.video.usuallyTakes') }} · {{ task.prompt }}</p>
        </div>

        <!-- succeeded: player -->
        <div v-else-if="task.state === 'succeeded'" class="space-y-2">
          <video v-if="task.url" :src="task.url" controls class="max-h-[360px] w-full rounded-lg bg-black"></video>
          <p v-else class="text-xs text-amber-700 dark:text-amber-300">{{ t('studio.video.noUrlHint') }}</p>
          <div class="flex items-center justify-between text-[11px] text-gray-500 dark:text-dark-400">
            <span class="truncate" :title="task.prompt">{{ task.prompt }}</span>
            <span class="shrink-0 rounded bg-primary-50 px-1.5 py-0.5 font-semibold text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">{{ formatUsd(task.estCost) }}</span>
          </div>
          <div class="flex gap-3 text-[11px] font-medium text-gray-500 dark:text-dark-400">
            <button v-if="task.url" type="button" class="text-primary-600 dark:text-primary-300" @click="downloadMedia(task.url, `tokenkey-${task.id}.mp4`)">{{ t('studio.video.download') }}</button>
            <a v-if="task.url" :href="task.url" target="_blank" rel="noopener" class="text-primary-600 dark:text-primary-300">{{ t('studio.video.open') }}</a>
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

    <!-- RIGHT: cost panel + button. Hidden when the group serves no video tier —
         no point showing a $0 panel and a dead Generate button. -->
    <div v-if="tiers.length" class="space-y-4">
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
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gatewayVideoSubmit } from '@/api/playground'
import { extractVideoTaskId, videoStateFromFetch, extractVideoUrl } from '@/constants/playgroundMedia.tk'
import {
  VIDEO_ASPECT_PRESETS,
  VIDEO_DURATION_MIN,
  VIDEO_DURATION_MAX,
  VIDEO_DURATION_DEFAULT,
  resolveAvailableTiers,
} from '@/constants/mediaTiers.tk'
import { estimateVideoCost, formatUsd } from '@/utils/mediaCostEstimate.tk'
import { downloadMedia } from '@/utils/studioDownload.tk'
import { classifyGatewayError, studioErrorI18nKey, type StudioErrorCode } from '@/utils/studioGatewayError.tk'
import { useMediaLibrary, type VideoTaskItem } from '@/composables/useMediaLibrary'
import { useVideoTaskPoll, requestVideoNotifyPermission, maybeNotify } from '@/composables/useVideoTaskPoll'
import type { ApiKey } from '@/types'

const props = defineProps<{
  apiKey: string
  gatewayBase: string
  availableIds: Set<string>
  balance: number
  userId: number | string
  keyId: number | null
  keys: ApiKey[]
  rateMultiplier: number
}>()
const emit = defineEmits<{ (e: 'spent'): void }>()

const { t } = useI18n()
const library = useMediaLibrary(props.userId)

const tiers = computed(() => resolveAvailableTiers('video', props.availableIds))
const selectedTierId = ref<string>('')
const selected = computed(() => tiers.value.find((r) => r.tier.id === selectedTierId.value) ?? null)

const duration = ref<number>(VIDEO_DURATION_DEFAULT)
const aspectId = ref<string>('') // '' = auto (no aspect_ratio sent — proven zero-extra-field path)
const prompt = ref('')
const userEditedPrompt = ref(false)
const sending = ref(false)
const errorMessage = ref('')
const errorCode = ref<StudioErrorCode | ''>('')
const lastEvent = ref('')

const estimate = computed(() => {
  if (!selected.value) return 0
  return estimateVideoCost({
    perSecond: selected.value.candidate.perSecond || 0,
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
  return t('studio.video.formula', { rate: formatUsd(selected.value.candidate.perSecond || 0), seconds: duration.value })
})

const poll = useVideoTaskPoll({
  gatewayBase: () => props.gatewayBase,
  resolveKey: (keyId) => props.keys.find((k) => k.id === keyId)?.key,
  patch: library.patchVideoTask,
  onTerminal: (task, state) => {
    emit('spent')
    if (state === 'succeeded') {
      if (task.url) {
        lastEvent.value = t('studio.video.doneToast')
        maybeNotify(t('studio.video.notifyTitle'), task.prompt)
      } else {
        // Succeeded upstream but no playable URL could be extracted — surface the
        // same hint the card shows instead of a false "ready" notification.
        lastEvent.value = t('studio.video.noUrlHint')
      }
    } else {
      lastEvent.value = t('studio.video.failedToast', { cost: formatUsd(task.estCost) })
    }
  },
})

function selectTier(id: string): void {
  selectedTierId.value = id
}
function applySamplePrompt(): void {
  if (userEditedPrompt.value) return
  if (selected.value) prompt.value = t(selected.value.tier.samplePromptKey)
}
watch(
  tiers,
  (list) => {
    if (!list.length) {
      selectedTierId.value = ''
      return
    }
    if (!list.some((r) => r.tier.id === selectedTierId.value)) selectedTierId.value = list[0].tier.id
  },
  { immediate: true }
)
watch(selected, () => applySamplePrompt(), { immediate: true })

function formatElapsed(s: number): string {
  const sec = Math.max(0, Math.round(s || 0))
  const m = String(Math.floor(sec / 60)).padStart(2, '0')
  const ss = String(sec % 60).padStart(2, '0')
  return `${m}:${ss}`
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
  library.removeVideoTask(id)
}

async function enableNotify(): Promise<void> {
  const ok = await requestVideoNotifyPermission()
  lastEvent.value = ok ? t('studio.video.notifyEnabled') : t('studio.video.notifyDenied')
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
      model: resolved.candidate.modelId,
      prompt: text,
      duration: duration.value,
      aspectRatio: aspectId.value || undefined,
    })
    const taskId = extractVideoTaskId(raw)
    if (!taskId) throw new Error(t('studio.video.noTaskId'))
    const state = videoStateFromFetch(raw)
    const task: VideoTaskItem = {
      id: taskId,
      prompt: text,
      model: resolved.candidate.modelId,
      vendorLabel: resolved.candidate.vendorLabel,
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

// Reattach polling for any in-flight task persisted across a reload.
onMounted(() => {
  for (const task of library.videoTasks.value) {
    if (task.state === 'processing') poll.resume(task)
  }
})
</script>
