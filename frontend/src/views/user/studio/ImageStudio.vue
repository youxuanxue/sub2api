<template>
  <div class="grid grid-cols-1 gap-5 lg:grid-cols-[340px_1fr_250px]">
    <!-- LEFT: orchestration -->
    <div class="space-y-4">
      <div v-if="models.length === 0" class="rounded-xl border border-dashed border-gray-300 bg-white/60 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400">
        {{ t('studio.image.modelEmpty') }}
        <router-link class="mt-1 block font-medium text-primary-600 underline dark:text-primary-400" to="/pricing">
          {{ t('studio.viewPricing') }}
        </router-link>
      </div>

      <!-- Transparent MODEL picker: the actual model is the choice, shown humanely
           (friendly name + price + vendor + raw id subtext + quality badge). -->
      <div v-else class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <div class="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ t('studio.image.modelLabel') }}</div>
        <div class="space-y-2">
          <button
            v-for="r in models"
            :key="r.model.modelId"
            type="button"
            class="w-full rounded-xl border p-3 text-left transition"
            :class="selectedModelId === r.model.modelId
              ? 'border-primary-500 bg-primary-50 ring-2 ring-primary-500/30 dark:border-primary-500 dark:bg-primary-950/40'
              : 'border-gray-200 hover:border-primary-300 dark:border-dark-600'"
            data-testid="studio-image-model"
            @click="selectedModelId = r.model.modelId"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-[13px] font-semibold text-gray-900 dark:text-white">{{ r.model.displayName }}</span>
              <span class="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-600 dark:bg-dark-800 dark:text-dark-300">{{ t(r.model.qualityBadgeKey) }}</span>
            </div>
            <div class="mt-1 flex items-center justify-between gap-2">
              <span class="text-[12px] font-bold text-primary-700 dark:text-primary-300">{{ formatUsd(r.baseImagePrice || 0) }}{{ t('studio.image.perImageUnit') }}</span>
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
          :placeholder="t('studio.image.promptPlaceholder')"
          :disabled="sending"
          @input="userEditedPrompt = true"
        />
        <div class="mt-3">
          <div class="mb-1.5 text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ t('studio.image.aspectLabel') }}</div>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="p in IMAGE_ASPECT_PRESETS"
              :key="p.id"
              type="button"
              class="rounded-lg border px-3 py-1.5 text-sm font-medium transition"
              :class="aspectId === p.id
                ? 'border-primary-600 bg-primary-600 text-white'
                : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-dark-300'"
              @click="aspectId = p.id"
            >
              {{ t(p.labelKey) }}
            </button>
          </div>
          <p class="mt-1.5 text-[11px] text-gray-400 dark:text-dark-500">
            {{ t('studio.image.billedAs', { tier: classifiedTier, mult: sizeMultiplier }) }}
          </p>
        </div>
        <div class="mt-3 flex items-center justify-between">
          <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('studio.image.count') }}</span>
          <div class="flex items-center gap-2">
            <button type="button" class="h-7 w-7 rounded-lg border border-gray-200 text-gray-600 hover:border-primary-300 disabled:opacity-40 dark:border-dark-600 dark:text-dark-300" :disabled="n <= IMAGE_N_MIN" @click="n = Math.max(IMAGE_N_MIN, n - 1)">−</button>
            <span class="w-6 text-center text-sm font-semibold tabular-nums">{{ n }}</span>
            <button type="button" class="h-7 w-7 rounded-lg border border-gray-200 text-gray-600 hover:border-primary-300 disabled:opacity-40 dark:border-dark-600 dark:text-dark-300" :disabled="n >= IMAGE_N_MAX" @click="n = Math.min(IMAGE_N_MAX, n + 1)">+</button>
          </div>
        </div>
        <!-- No Advanced panel for image: imagen / seedream adaptors honor no
             tunable params beyond size + count (verified against new-api). -->
      </div>
    </div>

    <!-- CENTER: results -->
    <div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
      <div class="mb-3 flex items-center justify-between">
        <span class="text-sm font-semibold text-gray-700 dark:text-dark-200">{{ t('studio.image.resultsTitle') }}</span>
        <button v-if="library.images.value.length" type="button" class="text-xs text-gray-400 hover:text-gray-700 dark:hover:text-dark-200" @click="library.clearImages()">
          {{ t('studio.clear') }}
        </button>
      </div>
      <div v-if="!library.images.value.length" class="py-16 text-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('studio.image.emptyHint') }}
      </div>
      <div v-else class="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <figure
          v-for="img in library.images.value"
          :key="img.id"
          class="group overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700"
        >
          <div class="relative">
            <a :href="img.src" target="_blank" rel="noopener">
              <img :src="img.src" :alt="img.prompt" class="aspect-square w-full object-cover" loading="lazy" />
            </a>
            <div class="absolute inset-x-0 bottom-0 flex items-center justify-center gap-1.5 bg-black/40 p-1.5 opacity-0 transition group-hover:opacity-100">
              <button type="button" class="rounded-md bg-white/90 px-2 py-1 text-[11px] font-medium text-gray-800 hover:bg-white" @click="download(img)">{{ t('studio.image.download') }}</button>
              <button type="button" class="rounded-md bg-white/90 px-2 py-1 text-[11px] font-medium text-gray-800 hover:bg-white" @click="reuse(img)">{{ t('studio.image.usePrompt') }}</button>
            </div>
          </div>
          <figcaption class="flex items-center justify-between gap-2 px-2.5 py-1.5 text-[11px] text-gray-500 dark:text-dark-400">
            <span class="truncate" :title="img.prompt">{{ img.prompt }}</span>
            <span class="shrink-0 rounded bg-primary-50 px-1.5 py-0.5 font-semibold text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">{{ formatUsd(img.cost) }}</span>
          </figcaption>
        </figure>
      </div>
    </div>

    <!-- RIGHT: cost panel + button (the spine). Hidden when the group serves no
         image model — no point showing a $0 panel and a dead Generate button. -->
    <div v-if="models.length" class="space-y-4">
      <div class="rounded-xl border border-primary-200 bg-primary-50/40 p-4 shadow-sm dark:border-primary-900/40 dark:bg-primary-950/30">
        <div class="text-xs font-semibold uppercase tracking-wide text-primary-700 dark:text-primary-300">{{ t('studio.cost.thisGeneration') }}</div>
        <div class="mt-2 font-mono text-[12px] text-gray-600 dark:text-dark-300">{{ formula }}</div>
        <div class="mt-3 space-y-1 border-t border-primary-200/60 pt-3 text-sm dark:border-primary-900/40">
          <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.estimate') }}</span><span class="font-bold text-gray-900 tabular-nums dark:text-white">{{ formatUsd(estimate) }}</span></div>
          <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.balance') }}</span><span class="tabular-nums text-gray-700 dark:text-dark-200">{{ formatUsd(balance) }}</span></div>
          <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.afterGeneration') }}</span><span class="tabular-nums" :class="canAfford ? 'text-gray-700 dark:text-dark-200' : 'text-red-600 dark:text-red-400'">{{ formatUsd(balance - estimate) }}</span></div>
        </div>
      </div>

      <button
        type="button"
        class="w-full rounded-xl bg-gradient-to-br from-primary-500 to-primary-700 px-4 py-3 text-sm font-bold text-white shadow-sm transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
        :disabled="!canGenerate"
        data-testid="studio-image-generate"
        @click="generate"
      >
        <template v-if="sending">{{ t('studio.image.generating') }}</template>
        <template v-else-if="!canAfford">{{ t('studio.image.generateTopUp', { cost: formatUsd(estimate) }) }}</template>
        <template v-else>{{ t('studio.image.generate', { cost: formatUsd(estimate) }) }}</template>
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
        data-testid="studio-image-error"
      >
        {{ errorMessage }}
        <router-link v-if="errorCode === 'insufficient_balance'" to="/purchase" class="ml-1 font-medium underline">{{ t('studio.topUp') }}</router-link>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gatewayImageGenerations } from '@/api/playground'
import { extractImageItems } from '@/constants/playgroundMedia.tk'
import {
  IMAGE_ASPECT_PRESETS,
  IMAGE_N_MIN,
  IMAGE_N_MAX,
  resolveAvailableModels,
  defaultModelId,
  type MediaPriceMap,
} from '@/constants/mediaTiers.tk'
import {
  classifyImageBillingTier,
  estimateImageCost,
  estimateImageHoldCost,
  formatUsd,
  IMAGE_SIZE_MULTIPLIER,
} from '@/utils/mediaCostEstimate.tk'
import { classifyGatewayError, studioErrorI18nKey, type StudioErrorCode } from '@/utils/studioGatewayError.tk'
import { downloadMedia } from '@/utils/studioDownload.tk'
import { useMediaLibrary, type ImageHistoryItem } from '@/composables/useMediaLibrary'

const props = defineProps<{
  apiKey: string
  gatewayBase: string
  availableIds: Set<string>
  priceMap: MediaPriceMap
  balance: number
  userId: number | string
  rateMultiplier: number
}>()
const emit = defineEmits<{ (e: 'spent'): void }>()

const { t } = useI18n()
const library = useMediaLibrary(props.userId)

const models = computed(() => resolveAvailableModels('image', props.availableIds, props.priceMap))
const selectedModelId = ref<string>('')
const selected = computed(() => models.value.find((r) => r.model.modelId === selectedModelId.value) ?? null)

const aspectId = ref<string>(IMAGE_ASPECT_PRESETS[0].id)
const aspectPreset = computed(() => IMAGE_ASPECT_PRESETS.find((p) => p.id === aspectId.value) ?? IMAGE_ASPECT_PRESETS[0])
const classifiedTier = computed(() => classifyImageBillingTier(aspectPreset.value.size))
const sizeMultiplier = computed(() => IMAGE_SIZE_MULTIPLIER[classifiedTier.value])

const n = ref(1)
const prompt = ref('')
const userEditedPrompt = ref(false)
const sending = ref(false)
const errorMessage = ref('')
const errorCode = ref<StudioErrorCode | ''>('')

const estimate = computed(() => {
  if (!selected.value) return 0
  return estimateImageCost({
    baseImagePrice: selected.value.baseImagePrice || 0,
    size: aspectPreset.value.size,
    n: n.value,
    rateMultiplier: props.rateMultiplier,
  })
})
// Affordability is gated on the backend HOLD upper bound (4K tier-max), NOT the
// per-size estimate — otherwise a balance between the headline price and the
// hold would enable a request the gateway then 403s (insufficient_balance).
const holdEstimate = computed(() => {
  if (!selected.value) return 0
  return estimateImageHoldCost({
    baseImagePrice: selected.value.baseImagePrice || 0,
    n: n.value,
    rateMultiplier: props.rateMultiplier,
  })
})
const canAfford = computed(() => holdEstimate.value <= props.balance)
const canGenerate = computed(
  () => !sending.value && !!props.apiKey && !!prompt.value.trim() && !!selected.value && canAfford.value
)
const formula = computed(() => {
  if (!selected.value) return ''
  const base = formatUsd(selected.value.baseImagePrice || 0)
  return t('studio.image.formula', { base, tier: classifiedTier.value, mult: sizeMultiplier.value, n: n.value })
})

function applySamplePrompt(): void {
  if (userEditedPrompt.value) return
  prompt.value = t('studio.image.samplePrompt')
}

// Pick the cheapest non-footgun model once models resolve.
watch(
  models,
  (list) => {
    if (!list.some((r) => r.model.modelId === selectedModelId.value)) {
      selectedModelId.value = defaultModelId(list) ?? ''
    }
  },
  { immediate: true }
)
watch(selected, () => applySamplePrompt(), { immediate: true })

function reuse(img: ImageHistoryItem): void {
  prompt.value = img.prompt
  userEditedPrompt.value = true
}

function download(img: ImageHistoryItem): void {
  downloadMedia(img.src, `tokenkey-${img.id}.png`)
}

async function generate(): Promise<void> {
  const text = prompt.value.trim()
  const resolved = selected.value
  if (!text || !props.apiKey || !resolved || sending.value || !canAfford.value) return
  errorMessage.value = ''
  errorCode.value = ''
  sending.value = true
  try {
    const raw = await gatewayImageGenerations(props.apiKey, props.gatewayBase, {
      model: resolved.servedId,
      prompt: text,
      size: aspectPreset.value.size,
      n: n.value,
    })
    const items = extractImageItems(raw)
    if (!items.length) throw new Error(t('studio.image.noResult'))
    const perImage = estimateImageCost({
      baseImagePrice: resolved.baseImagePrice || 0,
      size: aspectPreset.value.size,
      n: 1,
      rateMultiplier: props.rateMultiplier,
    })
    const ts = Date.now()
    const history: ImageHistoryItem[] = items.map((it, i) => ({
      id: `${ts}-${i}-${Math.round(perImage * 1e6)}`,
      src: it.src,
      prompt: text,
      revisedPrompt: it.revisedPrompt,
      model: resolved.servedId,
      vendorLabel: resolved.model.vendorLabel,
      size: aspectPreset.value.size,
      cost: perImage,
      ts,
    }))
    library.addImages(history)
    emit('spent')
  } catch (e) {
    const msg = e instanceof Error ? e.message : t('studio.errors.generic')
    const code = classifyGatewayError(msg)
    errorCode.value = code
    errorMessage.value = code === 'generic' ? msg : t(studioErrorI18nKey(code))
  } finally {
    sending.value = false
  }
}
</script>
