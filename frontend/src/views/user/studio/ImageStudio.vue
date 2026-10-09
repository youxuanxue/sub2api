<template>
  <div class="grid grid-cols-1 gap-5 lg:grid-cols-[360px_1fr]">
    <!-- LEFT: orchestration -->
    <div class="space-y-4">
      <div v-if="catalogLoading && models.length === 0" class="rounded-xl border border-dashed border-gray-300 bg-white/60 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400" data-testid="studio-image-model-loading">
        {{ t('studio.loadingModels') }}
      </div>
      <div v-else-if="models.length === 0" class="rounded-xl border border-dashed border-gray-300 bg-white/60 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400">
        {{ t('studio.image.modelEmpty') }}
        <router-link class="mt-1 block font-medium text-primary-600 underline dark:text-primary-400" to="/models?view=pricing">
          {{ t('studio.viewPricing') }}
        </router-link>
      </div>

      <StudioModelPicker
        v-else
        v-model="selectedModelId"
        :label="t('studio.image.modelLabel')"
        :models="pickerModels"
        test-id="studio-image-model"
      />

      <div v-if="models.length" class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <StudioPromptTextarea
          v-model="prompt"
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
          <ImageGenerationParameters v-model="imageOptions" :plan="generationPlan" :disabled="sending" aspect-test-id="studio-image-aspect" />
          <p class="mt-1.5 text-[11px] text-gray-400 dark:text-dark-500">
            <template v-if="pricesFlat">{{ t('studio.image.billedFlat') }}</template>
            <template v-else>{{ t('studio.image.billedAs', { tier: classifiedTier, mult: sizeMultiplier }) }}</template>
          </p>
        </div>
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
            <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.estimate') }}</span><span class="font-bold text-gray-900 tabular-nums dark:text-white">{{ estimateLabel }}</span></div>
            <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.balance') }}</span><span class="tabular-nums text-gray-700 dark:text-dark-200">{{ formatUsd(balance) }}</span></div>
            <div class="flex justify-between"><span class="text-gray-500 dark:text-dark-400">{{ t('studio.cost.afterGeneration') }}</span><span class="tabular-nums" :class="canAfford ? 'text-gray-700 dark:text-dark-200' : 'text-red-600 dark:text-red-400'">{{ afterLabel }}</span></div>
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
          <template v-else-if="!canAfford">{{ t('studio.image.generateTopUp', { cost: estimateLabel }) }}</template>
          <template v-else>{{ t('studio.image.generate', { cost: estimateLabel }) }}</template>
        </button>
        <router-link
          v-if="!canAfford"
          to="/purchase"
          class="block text-center text-xs font-medium text-primary-600 underline dark:text-primary-400"
        >
          {{ t('studio.topUp') }}
        </router-link>

        <StudioGatewayError
          v-if="errorMessage"
          :message="errorMessage"
          :code="errorCode"
          data-testid="studio-image-error"
        />
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
      <StudioLocalSaveBanner v-if="library.images.value.length" test-id="studio-image-save-reminder" class="mb-3" />
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
            <template v-if="imageHistoryItemAvailable(img)">
              <button type="button" class="block w-full cursor-zoom-in" :title="t('studio.image.enlargeHint')" data-testid="studio-image-thumb" @click="openPreview(img)">
                <img
                  :src="img.src"
                  :alt="img.prompt"
                  class="aspect-square w-full object-cover"
                  loading="lazy"
                  @error="onThumbError(img)"
                />
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
            <StudioImageExpired v-else>
              <button type="button" class="mt-1 rounded-md bg-white/90 px-2 py-0.5 text-[10px] font-medium text-gray-700 ring-1 ring-gray-200 hover:bg-white dark:bg-dark-700 dark:text-dark-200 dark:ring-dark-600" @click="reuse(img)">{{ t('studio.image.usePrompt') }}</button>
            </StudioImageExpired>
          </div>
          <figcaption class="flex items-center justify-between gap-2 px-2.5 py-1.5 text-[11px] text-gray-500 dark:text-dark-400">
            <div class="min-w-0 flex-1">
              <span class="block truncate" :title="imagePromptTitle(img)">{{ img.prompt }}</span>
              <span class="block truncate font-mono text-[10px] text-gray-400 dark:text-dark-500" :title="img.model">{{ img.model }}</span>
            </div>
            <span class="shrink-0 rounded bg-primary-50 px-1.5 py-0.5 font-semibold text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">{{ formatUsd(img.cost) }}</span>
          </figcaption>
        </figure>
      </div>
    </div>

    <StudioImagePreviewLightbox
      :preview="preview"
      :show-use-as-input="supportsImageInput"
      @close="closePreview"
      @download="download"
      @reuse="reuseAndClose"
      @use-as-input="useAsInputAndClose"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import type { APIKeyCapabilityModel } from '@/api/api-key-capabilities'
import ImageGenerationParameters from '@/components/keys/ImageGenerationParameters.vue'
import { imageGenerationPlan, normalizeImageOptions, extractGeneratedImages } from '@/utils/imageGeneration.tk'
import { useI18n } from 'vue-i18n'
import { gatewayGenerateImage, gatewayImageToPrompt, gatewayTraceRunId } from '@/api/playground'
import { pickVisionChatModel } from '@/constants/playgroundMedia.tk'
import ImageUpload from '@/components/common/ImageUpload.vue'
import {
  resolveAvailableModels,
  defaultModelId,
  type MediaPriceMap,
} from '@/constants/studioMediaPresentations.tk'
import {
  classifyImageBillingTier,
  estimateImageCost,
  estimateImageHoldCost,
  formatUsd,
  IMAGE_SIZE_MULTIPLIER,
} from '@/utils/mediaCostEstimate.tk'
import StudioGatewayError from '@/views/user/studio/components/StudioGatewayError.vue'
import { classifyGatewayError, studioErrorI18nKey, type StudioErrorCode } from '@/utils/studioGatewayError.tk'
import { useStudioImageCardActions } from '@/composables/useStudioImageCardActions'
import { imageHistoryItemAvailable } from '@/utils/studioMedia.tk'
import {
  imageHistoryPromptTitle,
  matchImageHistoryModel,
  studioImageHistoryId,
} from '@/utils/studioImageHistory.tk'
import { mountStudioImageLibrary, onStudioImageThumbError } from '@/composables/useStudioImageLibrary'
import { useStudioImagePreview } from '@/composables/useStudioImagePreview'
import StudioLocalSaveBanner from '@/views/user/studio/components/StudioLocalSaveBanner.vue'
import StudioImageExpired from '@/views/user/studio/components/StudioImageExpired.vue'
import StudioImagePreviewLightbox from '@/views/user/studio/components/StudioImagePreviewLightbox.vue'
import StudioModelPicker from '@/views/user/studio/components/StudioModelPicker.vue'
import StudioPromptTextarea from '@/views/user/studio/components/StudioPromptTextarea.vue'
import { useMediaLibrary, type ImageHistoryItem } from '@/composables/useMediaLibrary'

const props = defineProps<{
  apiKey: string
  gatewayBase: string
  availableIds: Set<string>
  priceMap: MediaPriceMap
  balance: number
  userId: number | string
  rateMultiplier: number
  catalogLoading?: boolean
  capabilityModels?: APIKeyCapabilityModel[]
}>()
const emit = defineEmits<{ (e: 'spent'): void }>()

const { t } = useI18n()
const library = useMediaLibrary(props.userId)
const { preview, openPreview, closePreview } = useStudioImagePreview()

const models = computed(() => resolveAvailableModels('image', props.availableIds, props.priceMap))
const selectedModelId = ref<string>('')
const selected = computed(() => models.value.find((r) => r.presentation.modelId === selectedModelId.value) ?? null)
const pickerModels = computed(() =>
  models.value.map((r) => ({
    modelId: r.presentation.modelId,
    displayName: r.presentation.displayName,
    qualityBadgeKey: r.presentation.qualityBadgeKey,
    priceLabel:
      r.baseImagePrice != null
        ? `${formatUsd(r.baseImagePrice)}${t('studio.image.perImageUnit')}`
        : t('studio.usagePriced'),
    vendorLabel: r.presentation.vendorLabel,
    servedId: r.servedId,
    needsApikeyAccount: r.presentation.needsApikeyAccount,
  }))
)

const route = useRoute()
const generationPlan = computed(() => imageGenerationPlan(selected.value?.servedId ?? '', props.capabilityModels?.find(m => m.id === selected.value?.servedId)?.image_generation))
const imageOptions = ref(normalizeImageOptions(generationPlan.value))
const sizeOptions = computed(() => generationPlan.value.sizes)
const sentSize = computed(() => sizeOptions.value.find(o => o.ratio === imageOptions.value.ratio)?.value ?? '')
const n = computed(() => imageOptions.value.n)
watch([generationPlan, () => props.catalogLoading], ([plan, loading]) => {
  if (!loading) imageOptions.value = normalizeImageOptions(plan, imageOptions.value)
}, { immediate: true })
const classifiedTier = computed(() => classifyImageBillingTier(sentSize.value))
const sizeMultiplier = computed(() => IMAGE_SIZE_MULTIPLIER[classifiedTier.value])
// Billing and request capabilities are independent.
const pricesFlat = computed(() => !!selected.value?.presentation.flatImageBilling || !!selected.value?.presentation.flatPricePerImage)
const effectiveN = n
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
const inputImage = computed({ get: () => imageOptions.value.inputImage, set: value => { imageOptions.value = normalizeImageOptions(generationPlan.value, { ...imageOptions.value, inputImage: value }) } })
const reversing = ref(false)
const supportsImageInput = computed(() => generationPlan.value.inputImage)
// A vision-capable gemini chat model from the group, used to describe an image
// back into a prompt. Independent of the selected image model.
const visionModelId = computed(() => pickVisionChatModel(props.availableIds))

const estimate = computed(() => {
  if (!selected.value || selected.value.baseImagePrice == null) return null
  return estimateImageCost({
    baseImagePrice: selected.value.baseImagePrice,
    size: pricesFlat.value ? '1K' : sentSize.value, // flat ⇒ ×1, no size tier
    n: effectiveN.value,
    rateMultiplier: props.rateMultiplier,
  })
})
// Affordability is gated on the backend HOLD upper bound. For imagen/seedream that
// is the 4K tier-max (settlement bills the real size), so the UI can't enable a
// request the gateway then 403s. Gemini bills flat per-image (no tier), so its hold
// IS the flat estimate. Missing live price → no fake $0 gate; backend hold decides.
const holdEstimate = computed(() => {
  if (!selected.value || selected.value.baseImagePrice == null) return null
  if (pricesFlat.value) {
    return estimateImageCost({
      baseImagePrice: selected.value.baseImagePrice,
      size: '1K',
      n: effectiveN.value, // gemini → 1 (n-locked); imagen → real n (multi-image, flat per image)
      rateMultiplier: props.rateMultiplier,
    })
  }
  return estimateImageHoldCost({
    baseImagePrice: selected.value.baseImagePrice,
    n: n.value,
    rateMultiplier: props.rateMultiplier,
  })
})
const canAfford = computed(() => holdEstimate.value == null || holdEstimate.value <= props.balance)
const canGenerate = computed(
  () => !props.catalogLoading && !sending.value && !reversing.value && !!props.apiKey && !!prompt.value.trim() && !!selected.value && canAfford.value
)
const estimateLabel = computed(() =>
  estimate.value == null ? t('studio.usagePriced') : formatUsd(estimate.value)
)
const afterLabel = computed(() =>
  estimate.value == null ? '—' : formatUsd(props.balance - estimate.value)
)
const formula = computed(() => {
  if (!selected.value) return ''
  if (selected.value.baseImagePrice == null) return t('studio.usagePriced')
  const base = formatUsd(selected.value.baseImagePrice)
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
    if (!list.length && props.catalogLoading) return
    if (!list.some((r) => r.presentation.modelId === selectedModelId.value)) {
      selectedModelId.value = defaultModelId(list) ?? ''
    }
  },
  { immediate: true }
)
watch(selected, () => applySamplePrompt(), { immediate: true })
// Navigation never generates; normalize against the selected key's current profile.
let journeyApplied = ''
watch([models, () => props.capabilityModels, () => props.catalogLoading, () => route.fullPath], () => {
  if (route.path !== '/studio') { journeyApplied = ''; return }
  if (journeyApplied === route.fullPath || props.catalogLoading) return
  const match = matchImageHistoryModel(models.value, String(route.query.model ?? ''))
  if (!match) return
  selectedModelId.value = match.presentation.modelId
  imageOptions.value = normalizeImageOptions(generationPlan.value, { ratio: String(route.query.ratio ?? ''), n: Number(route.query.n ?? 1) })
  journeyApplied = route.fullPath
}, { immediate: true })

function reuse(img: ImageHistoryItem): void {
  prompt.value = img.prompt
  userEditedPrompt.value = true
  const match = matchImageHistoryModel(models.value, img.model)
  if (match) selectedModelId.value = match.presentation.modelId
  imageOptions.value = normalizeImageOptions(generationPlan.value, { ratio: img.size, n: 1 })
}

function imagePromptTitle(img: ImageHistoryItem): string {
  return imageHistoryPromptTitle(img, (text) => t('studio.image.revisedPromptHint', { text }))
}

function onThumbError(img: ImageHistoryItem): void {
  void onStudioImageThumbError(library, img.id)
}

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

// Card/lightbox download rides the shared Studio image owner
// (useStudioImageCardActions — image sibling of useStudioVideoCardActions).
const { downloadCardImage: download, downloadAllImages } = useStudioImageCardActions()

function reuseAndClose(img: ImageHistoryItem): void {
  reuse(img)
  closePreview()
}

onMounted(() => {
  void mountStudioImageLibrary(props.apiKey, props.gatewayBase, library)
})

// Batch export order matches the on-screen grid (newest first); the stagger
// that keeps browsers from dropping burst downloads lives in the owner.
function downloadAll(): void {
  downloadAllImages(library.images.value)
}

async function generate(): Promise<void> {
  const text = prompt.value.trim()
  const resolved = selected.value
  if (!text || !props.apiKey || !resolved || sending.value || !canAfford.value) return
  errorMessage.value = ''
  errorCode.value = ''
  sending.value = true
  const trace = { studioSource: 'studio.image', studioRunId: gatewayTraceRunId('studio-image') }
  const submittedSize = sentSize.value
  const perImage = estimateImageCost({
    baseImagePrice: resolved.baseImagePrice || 0,
    size: pricesFlat.value ? '1K' : submittedSize,
    n: 1,
    rateMultiplier: props.rateMultiplier,
  })
  try {
    const plan = generationPlan.value
    const raw = await gatewayGenerateImage(props.apiKey, props.gatewayBase, resolved.servedId, text, plan, imageOptions.value, trace)
    const items = extractGeneratedImages(raw, plan)
    if (!items.length) throw new Error(t('studio.image.noResult'))
    const ts = Date.now()
    const history: ImageHistoryItem[] = items.map((it) => ({
      id: studioImageHistoryId(),
      src: it.src,
      s3Key: it.s3Key,
      prompt: text,
      revisedPrompt: it.revisedPrompt,
      model: resolved.servedId,
      vendorLabel: resolved.presentation.vendorLabel,
      size: submittedSize,
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
