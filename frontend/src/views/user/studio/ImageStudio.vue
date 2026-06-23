<template>
  <div class="grid grid-cols-1 gap-5 lg:grid-cols-[360px_1fr]">
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
        <!-- Image-to-image (图生图): only gemini-native models consume an input
             image (chat path). Upload one or "use as input" a library image, then
             describe the change above. The same image can be reverse-prompted. -->
        <div v-if="supportsImageInput" class="mt-3 space-y-2 rounded-lg border border-dashed border-gray-200 p-3 dark:border-dark-700">
          <div class="text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ t('studio.image.inputImageLabel') }}</div>
          <ImageUpload
            :model-value="inputImage"
            mode="image"
            size="md"
            :upload-label="t('studio.image.inputUpload')"
            :remove-label="t('studio.image.inputRemove')"
            :hint="t('studio.image.inputHint')"
            :max-size="INPUT_IMAGE_MAX_BYTES"
            :downscale-max-edge="INPUT_IMAGE_DOWNSCALE_EDGE"
            @update:model-value="inputImage = $event"
          />
          <button
            v-if="inputImage && visionModelId"
            type="button"
            class="text-xs font-medium text-primary-600 hover:text-primary-700 disabled:opacity-50 dark:text-primary-400 dark:hover:text-primary-300"
            :disabled="reversing || sending"
            data-testid="studio-image-reverse-prompt"
            @click="reversePrompt"
          >
            {{ reversing ? t('studio.image.reversing') : t('studio.image.reversePrompt') }}
          </button>
        </div>
        <div class="mt-3">
          <!-- Aspect picker is shown only when the selected model has supported ratios
               (Imagen ratio codes / Seedream pixel sizes). Gemini-native image has no
               picker: the ratio control is not honored on its serving path (see #807
               R-001), so we omit it rather than ship a cosmetic control. -->
          <template v-if="sizeOptions.length">
            <div class="mb-1.5 text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ t('studio.image.aspectLabel') }}</div>
            <div class="flex flex-wrap gap-2">
              <button
                v-for="opt in sizeOptions"
                :key="opt.ratio"
                type="button"
                class="rounded-lg border px-3 py-1.5 text-left transition"
                :class="selectedRatio === opt.ratio
                  ? 'border-primary-600 bg-primary-600 text-white'
                  : 'border-gray-200 text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:text-dark-300'"
                data-testid="studio-image-aspect"
                @click="selectedRatio = opt.ratio"
              >
                <div class="text-sm font-medium tabular-nums">{{ opt.ratio }}</div>
                <div v-if="sizeSubtext(opt.value, opt.ratio)" class="text-[10px] tabular-nums opacity-70">{{ sizeSubtext(opt.value, opt.ratio) }}</div>
              </button>
            </div>
          </template>
          <p class="mt-1.5 text-[11px] text-gray-400 dark:text-dark-500">
            <template v-if="pricesFlat">{{ t('studio.image.billedFlat') }}</template>
            <template v-else>{{ t('studio.image.billedAs', { tier: classifiedTier, mult: sizeMultiplier }) }}</template>
          </p>
        </div>
        <!-- Count stepper hidden for gemini-native image: the chat surface returns one image per request. -->
        <div v-if="!isFlatImage" class="mt-3 flex items-center justify-between">
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

      <!-- Cost + primary CTA. Moved out of a dedicated right column into the
           orchestration stack so the action lives where composing ends — one
           vertical sweep (model → prompt → aspect → count → cost → Generate)
           instead of an eye-jump across the results gallery to a far-right button. -->
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

    <!-- CENTER: results -->
    <div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
      <div class="mb-3 flex items-center justify-between">
        <span class="text-sm font-semibold text-gray-700 dark:text-dark-200">{{ t('studio.image.resultsTitle') }}</span>
        <div v-if="library.images.value.length" class="flex items-center gap-3">
          <button type="button" class="text-xs font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300" data-testid="studio-image-download-all" @click="downloadAll">
            {{ t('studio.image.downloadAll') }}
          </button>
          <button type="button" class="text-xs text-gray-400 hover:text-gray-700 dark:hover:text-dark-200" @click="library.clearImages()">
            {{ t('studio.clear') }}
          </button>
        </div>
      </div>
      <div v-if="!library.images.value.length" class="py-16 text-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('studio.image.emptyHint') }}
      </div>
      <div v-else class="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-4">
        <figure
          v-for="img in library.images.value"
          :key="img.id"
          class="group overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700"
        >
          <div class="relative">
            <!-- Click to enlarge IN-PAGE. A plain <a target="_blank"> to img.src
                 breaks for gemini-native images: their src is a data: URI and
                 browsers block top-level navigation to data: URLs (→ about:blank,
                 the "click shows nothing" report). A lightbox previews every src
                 (data: or http) without leaving the page. -->
            <template v-if="img.src">
              <button type="button" class="block w-full cursor-zoom-in" :title="t('studio.image.enlargeHint')" data-testid="studio-image-thumb" @click="openPreview(img)">
                <img :src="img.src" :alt="img.prompt" class="aspect-square w-full object-cover" loading="lazy" />
              </button>
              <div class="pointer-events-none absolute inset-x-0 bottom-0 flex items-center justify-center gap-1.5 bg-black/40 p-1.5 opacity-0 transition group-hover:pointer-events-auto group-hover:opacity-100">
                <button type="button" class="rounded-md bg-white/90 px-2 py-1 text-[11px] font-medium text-gray-800 hover:bg-white" @click="download(img)">{{ t('studio.image.download') }}</button>
                <button type="button" class="rounded-md bg-white/90 px-2 py-1 text-[11px] font-medium text-gray-800 hover:bg-white" @click="reuse(img)">{{ t('studio.image.usePrompt') }}</button>
                <button v-if="supportsImageInput" type="button" class="rounded-md bg-white/90 px-2 py-1 text-[11px] font-medium text-gray-800 hover:bg-white" data-testid="studio-image-use-as-input" @click="useAsInput(img)">{{ t('studio.image.useAsInput') }}</button>
              </div>
            </template>
            <!-- Reloaded inline image: its bytes were delivered ONCE and intentionally
                 not persisted to localStorage (#944 pass-through default — the gateway
                 does not rehost generated images), so after a reload there is no
                 thumbnail to show. Offer a regenerate path instead of a broken <img>. -->
            <div v-else class="flex aspect-square w-full flex-col items-center justify-center gap-1 bg-gray-50 px-2 text-center dark:bg-dark-800" data-testid="studio-image-expired">
              <span aria-hidden="true" class="text-2xl text-gray-300 dark:text-dark-600">🖼</span>
              <span class="text-[10px] leading-tight text-gray-400 dark:text-dark-500">{{ t('studio.image.expiredReload') }}</span>
              <button type="button" class="mt-1 rounded-md bg-white/90 px-2 py-0.5 text-[10px] font-medium text-gray-700 ring-1 ring-gray-200 hover:bg-white dark:bg-dark-700 dark:text-dark-200 dark:ring-dark-600" @click="reuse(img)">{{ t('studio.image.usePrompt') }}</button>
            </div>
          </div>
          <figcaption class="flex items-center justify-between gap-2 px-2.5 py-1.5 text-[11px] text-gray-500 dark:text-dark-400">
            <span class="truncate" :title="img.prompt">{{ img.prompt }}</span>
            <span class="shrink-0 rounded bg-primary-50 px-1.5 py-0.5 font-semibold text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">{{ formatUsd(img.cost) }}</span>
          </figcaption>
        </figure>
      </div>
    </div>

    <!-- Lightbox: full-resolution in-page preview (replaces the broken open-in-new-tab). -->
    <Teleport to="body">
      <div
        v-if="preview"
        class="fixed inset-0 z-[100] flex flex-col bg-black/85 backdrop-blur-sm"
        data-testid="studio-image-preview"
        @click.self="closePreview"
      >
        <div class="flex items-center justify-end p-3">
          <button type="button" class="rounded-lg bg-white/10 px-3 py-1.5 text-sm font-medium text-white hover:bg-white/20" data-testid="studio-image-preview-close" @click="closePreview">
            {{ t('studio.image.close') }} ✕
          </button>
        </div>
        <div class="flex min-h-0 flex-1 items-center justify-center px-4" @click.self="closePreview">
          <img :src="preview.src" :alt="preview.prompt" class="max-h-full max-w-full rounded-lg object-contain shadow-2xl" />
        </div>
        <div class="flex flex-wrap items-center justify-center gap-3 p-4">
          <span class="max-w-[60vw] truncate text-xs text-white/80" :title="preview.prompt">{{ preview.prompt }}</span>
          <span class="shrink-0 rounded bg-white/15 px-1.5 py-0.5 text-[11px] font-semibold text-white">{{ formatUsd(preview.cost) }}</span>
          <button type="button" class="rounded-md bg-white px-3 py-1.5 text-[12px] font-medium text-gray-900 hover:bg-gray-100" @click="download(preview)">{{ t('studio.image.download') }}</button>
          <button type="button" class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white" @click="reuseAndClose(preview)">{{ t('studio.image.usePrompt') }}</button>
          <button v-if="supportsImageInput" type="button" class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white" @click="useAsInputAndClose(preview)">{{ t('studio.image.useAsInput') }}</button>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gatewayImageGenerations, gatewayGeminiImageViaChat, gatewayImageToPrompt, gatewayImagePresign } from '@/api/playground'
import { extractImageItems, extractChatImageItems, pickVisionChatModel } from '@/constants/playgroundMedia.tk'
import ImageUpload from '@/components/common/ImageUpload.vue'
import {
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

// Aspect options are MODEL-SPECIFIC: each model declares the ratios its upstream
// accepts (Imagen ⇒ ratio codes; Seedream ⇒ pixel WxH). We track the chosen RATIO
// so it survives a model switch (both vendors expose the same ratio labels), and
// put the option's exact `value` on the wire.
const sizeOptions = computed(() => selected.value?.model.imageSizes ?? [])
const selectedRatio = ref<string>('')
const selectedSize = computed(
  () => sizeOptions.value.find((o) => o.ratio === selectedRatio.value) ?? sizeOptions.value[0] ?? null
)
// Exact string sent as the request `size` (ratio code or WxH — never wrapped).
const sentSize = computed(() => selectedSize.value?.value ?? '')
const classifiedTier = computed(() => classifyImageBillingTier(sentSize.value))
const sizeMultiplier = computed(() => IMAGE_SIZE_MULTIPLIER[classifiedTier.value])
// Gemini-native image is served via /v1/chat/completions and bills a FLAT
// output_cost_per_image (no 1K/2K/4K size tier, one image per request). Skip the
// size-tier multiplier and the count stepper for these models.
const isFlatImage = computed(() => !!selected.value?.model.flatImageBilling)
// Flat-PRICED: no 1K/2K/4K size-tier multiplier (imagen bills Google's flat
// official price; gemini-native is flat too). DECOUPLED from isFlatImage — which
// additionally implies chat routing / n=1 / image-input — so imagen keeps n>1,
// no image-input, /v1/images routing, but escapes the size multiplier. Mirrors
// backend tkIsFlatPerImageModel.
const pricesFlat = computed(() => isFlatImage.value || !!selected.value?.model.flatPricePerImage)
const effectiveN = computed(() => (isFlatImage.value ? 1 : n.value))
// Show the literal pixel size as a subtext when it differs from the ratio label
// (Seedream); for Imagen the value IS the ratio, so no redundant subtext.
function sizeSubtext(value: string, ratio: string): string {
  return value === ratio ? '' : value.replace('x', '×')
}

const n = ref(1)
const prompt = ref('')
const userEditedPrompt = ref(false)
const sending = ref(false)
const errorMessage = ref('')
const errorCode = ref<StudioErrorCode | ''>('')

// Image-to-image (图生图) + reverse-prompt (图→prompt). Both ride the gemini-native
// chat path, so the input-image slot is offered only for those models (imagen /
// seedream take no input image on /v1/images/generations). The input image is a
// data: URI (fresh upload) or a library image's src (reuse).
const INPUT_IMAGE_MAX_BYTES = 4 * 1024 * 1024 // 4 MB — well within gemini inline-image limits
// Downscale the input image to this max edge (px) before it travels inline through the
// gateway: image-to-image / reverse-prompt / first-frame inputs don't need full res
// (gemini inline-image guidance is well under 4 MB), so this cuts request-body bytes
// by an order of magnitude. The 4 MB cap above still guards the original upload.
const INPUT_IMAGE_DOWNSCALE_EDGE = 1536
const inputImage = ref('')
const reversing = ref(false)
const supportsImageInput = computed(() => isFlatImage.value)
// A vision-capable gemini chat model from the group, used to describe an image
// back into a prompt. Independent of the selected image model.
const visionModelId = computed(() => pickVisionChatModel(props.availableIds))

const estimate = computed(() => {
  if (!selected.value) return 0
  return estimateImageCost({
    baseImagePrice: selected.value.baseImagePrice || 0,
    size: pricesFlat.value ? '1K' : sentSize.value, // flat ⇒ ×1, no size tier
    n: effectiveN.value,
    rateMultiplier: props.rateMultiplier,
  })
})
// Affordability is gated on the backend HOLD upper bound. For imagen/seedream that
// is the 4K tier-max (settlement bills the real size), so the UI can't enable a
// request the gateway then 403s. Gemini bills flat per-image (no tier), so its hold
// IS the flat estimate.
const holdEstimate = computed(() => {
  if (!selected.value) return 0
  if (pricesFlat.value) {
    return estimateImageCost({
      baseImagePrice: selected.value.baseImagePrice || 0,
      size: '1K',
      n: effectiveN.value, // gemini → 1 (n-locked); imagen → real n (multi-image, flat per image)
      rateMultiplier: props.rateMultiplier,
    })
  }
  return estimateImageHoldCost({
    baseImagePrice: selected.value.baseImagePrice || 0,
    n: n.value,
    rateMultiplier: props.rateMultiplier,
  })
})
const canAfford = computed(() => holdEstimate.value <= props.balance)
const canGenerate = computed(
  () => !sending.value && !reversing.value && !!props.apiKey && !!prompt.value.trim() && !!selected.value && canAfford.value
)
const formula = computed(() => {
  if (!selected.value) return ''
  const base = formatUsd(selected.value.baseImagePrice || 0)
  if (pricesFlat.value) {
    return t('studio.image.formulaFlat', { base, n: effectiveN.value })
  }
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
// Keep the chosen ratio valid as the model (and thus its option set) changes:
// preserve it when the new model also offers it, else fall back to the first.
watch(
  sizeOptions,
  (opts) => {
    if (!opts.some((o) => o.ratio === selectedRatio.value)) {
      selectedRatio.value = opts[0]?.ratio ?? ''
    }
  },
  { immediate: true }
)

function reuse(img: ImageHistoryItem): void {
  prompt.value = img.prompt
  userEditedPrompt.value = true
}

// Drop a staged input image when switching to a model that can't consume it,
// so an imagen/seedream request never carries a silently-ignored image.
watch(supportsImageInput, (ok) => {
  if (!ok) inputImage.value = ''
})

// Reuse a generated image as the image-to-image input (its src is a data:/http URI).
function useAsInput(img: ImageHistoryItem): void {
  inputImage.value = img.src
}
function useAsInputAndClose(img: ImageHistoryItem): void {
  useAsInput(img)
  closePreview()
}

// Reverse-prompt: describe the staged input image back into the prompt box.
async function reversePrompt(): Promise<void> {
  const model = visionModelId.value
  if (!inputImage.value || !model || !props.apiKey || reversing.value) return
  errorMessage.value = ''
  errorCode.value = ''
  reversing.value = true
  try {
    const text = await gatewayImageToPrompt(props.apiKey, props.gatewayBase, { model, image: inputImage.value })
    if (!text) throw new Error(t('studio.image.reverseEmpty'))
    prompt.value = text
    userEditedPrompt.value = true
    emit('spent') // a vision call was billed — refresh the balance like generate() does
  } catch (e) {
    const msg = e instanceof Error ? e.message : t('studio.errors.generic')
    const code = classifyGatewayError(msg)
    errorCode.value = code
    errorMessage.value = code === 'generic' ? msg : t(studioErrorI18nKey(code))
  } finally {
    reversing.value = false
  }
}

function download(img: ImageHistoryItem): void {
  downloadMedia(img.src, `tokenkey-${img.id}.png`)
}

// In-page lightbox: clicking a thumbnail opens the full image here instead of a
// new tab (data: URIs can't be navigated to top-level — they 404 to about:blank).
const preview = ref<ImageHistoryItem | null>(null)
function openPreview(img: ImageHistoryItem): void {
  // A reloaded inline image has an empty src (its bytes were not persisted); there
  // is nothing to enlarge, so don't open an empty lightbox.
  if (!img.src) return
  preview.value = img
}
function closePreview(): void {
  preview.value = null
}
function reuseAndClose(img: ImageHistoryItem): void {
  reuse(img)
  closePreview()
}
function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape' && preview.value) closePreview()
}
/**
 * Re-mint fresh presigned URLs for persisted offloaded images. Studio history is
 * localStorage-backed, but a presigned URL is intentionally short-lived, so on a
 * reload the stored `src` for an offloaded image may have expired (broken <img>).
 * For every persisted image carrying an s3Key, re-presign from the key (no
 * re-generation, no re-bill) and patch `src`.
 * Best-effort — a failure keeps the cached URL, at worst a stale thumbnail.
 */
async function refreshOffloadedImageUrls(): Promise<void> {
  if (!props.apiKey) return
  const stale = library.images.value.filter((it) => it.s3Key)
  if (!stale.length) return
  await Promise.all(
    stale.map(async (it) => {
      try {
        const url = await gatewayImagePresign(props.apiKey, props.gatewayBase, it.s3Key as string)
        if (url) it.src = url // deep watch in useMediaLibrary persists the refresh
      } catch {
        /* keep the cached URL — history is a convenience, not correctness */
      }
    })
  )
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  void refreshOffloadedImageUrls()
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

// Batch export: browsers throttle a burst of synchronous downloads, so stagger
// each save. Order matches the on-screen grid (newest first).
function downloadAll(): void {
  const imgs = library.images.value
  imgs.forEach((img, i) => {
    window.setTimeout(() => download(img), i * 350)
  })
}

async function generate(): Promise<void> {
  const text = prompt.value.trim()
  const resolved = selected.value
  if (!text || !props.apiKey || !resolved || sending.value || !canAfford.value) return
  errorMessage.value = ''
  errorCode.value = ''
  sending.value = true
  try {
    // Gemini-native image rides /v1/chat/completions (image returned as markdown in
    // the chat response — the universal path that works for antigravity + newapi
    // groups); imagen/seedream ride /v1/images/generations. Route by the model.
    let items
    if (isFlatImage.value) {
      const raw = await gatewayGeminiImageViaChat(props.apiKey, props.gatewayBase, {
        model: resolved.servedId,
        prompt: text,
        aspectRatio: sentSize.value, // ratio code → extra_body.google.image_config.aspect_ratio
        inputImage: inputImage.value || undefined // image-to-image when an input image is staged
      })
      items = extractChatImageItems(raw)
    } else {
      const raw = await gatewayImageGenerations(props.apiKey, props.gatewayBase, {
        model: resolved.servedId,
        prompt: text,
        size: sentSize.value,
        n: n.value,
      })
      items = extractImageItems(raw)
    }
    if (!items.length) throw new Error(t('studio.image.noResult'))
    const perImage = estimateImageCost({
      baseImagePrice: resolved.baseImagePrice || 0,
      size: pricesFlat.value ? '1K' : sentSize.value,
      n: 1,
      rateMultiplier: props.rateMultiplier,
    })
    const ts = Date.now()
    const history: ImageHistoryItem[] = items.map((it, i) => ({
      id: `${ts}-${i}-${Math.round(perImage * 1e6)}`,
      src: it.src,
      s3Key: it.s3Key,
      prompt: text,
      revisedPrompt: it.revisedPrompt,
      model: resolved.servedId,
      vendorLabel: resolved.model.vendorLabel,
      size: sentSize.value,
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
