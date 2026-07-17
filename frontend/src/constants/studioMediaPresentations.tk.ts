/**
 * TokenKey-only: Studio media presentation + resolver helpers.
 *
 * **Membership SSOT** (which image/video models exist for a key) lives in the
 * pricing catalogs (`GET /api/v1/public/pricing`, `GET /api/v1/me/pricing-catalog`)
 * via `billing_mode: image | video` — see `utils/studioMediaCatalog.tk.ts`.
 *
 * **This file** holds presentation-only metadata (display names, aspect ratios,
 * discrete video durations, verified adaptor params). A model appears in Studio
 * only when catalog membership ∩ key entitlement ∩ live price all agree.
 */

import { modalityForModel } from '@/constants/playgroundMedia.tk'
import type { CatalogBillingIndex } from '@/utils/studioMediaCatalog.tk'

export type StudioModality = 'image' | 'video'

/**
 * The modality axis the Studio SHELL reasons about for key selection. Chat is a
 * peer Studio tab (folded in from the retired /playground), but it has no media
 * billing-mode catalog row — a key "serves chat" when its /v1/models pool exposes
 * any chat-classified id (modalityForModel). image/video use catalog billing_mode.
 * Bake-off reports its active sub-modality to the shell, so the selected key
 * still tracks image vs video just like the dedicated tabs.
 */
export type PickerModality = StudioModality | 'chat'

const VERTEX = 'Google Vertex'
const VOLC = 'VolcEngine'
const GEMINI = 'Google Gemini'
const XAI = 'xAI'

const VENDOR_LABELS: Record<string, string> = {
  xai: XAI,
  vertex_ai: VERTEX,
  volcengine: VOLC,
  google: GEMINI,
  gemini: GEMINI,
}

function formatVendorLabel(vendor?: string): string {
  if (!vendor) return ''
  const key = vendor.trim().toLowerCase()
  return VENDOR_LABELS[key] ?? vendor
}

function defaultDisplayName(modelId: string): string {
  return modelId
    .split('-')
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ')
}

/**
 * True when this key's pool backs at least one catalog-listed media model of
 * `modality`. Uses the public pricing catalog's billing_mode index (loaded once
 * at Studio bootstrap) — not the presentation table below.
 */
export function hasCatalogMediaModality(
  modality: StudioModality,
  availableIds: ReadonlySet<string>,
  catalogBilling: CatalogBillingIndex
): boolean {
  for (const id of availableIds) {
    if (catalogBilling.get(id) === modality) return true
  }
  return false
}

/**
 * Whether this group's pool serves `modality` for the SHELL's key picker.
 * Dispatches chat (any chat-classified id in the pool) vs media (catalog
 * billing_mode). Bake-off passes its active image/video sub-modality, so
 * the shell keeps the selected key aligned with the child mode.
 */
export function groupServes(
  modality: PickerModality,
  availableIds: ReadonlySet<string>,
  catalogBilling: CatalogBillingIndex
): boolean {
  if (modality === 'chat') {
    for (const id of availableIds) {
      if (catalogBilling.has(id)) continue
      if (modalityForModel(id) === 'chat') return true
    }
    return false
  }
  return hasCatalogMediaModality(modality, availableIds, catalogBilling)
}

/** One selectable key, reduced to what the modality-aware picker needs. */
export interface ModalityKeyOption {
  id: number
  /** A key literally named "trial" is the historical default landing key. */
  isTrial: boolean
  /** Model ids exposed by this key's group, or universal entitlement ids. */
  availableIds: ReadonlySet<string>
}

/**
 * Pick the key the Studio should land on for `modality`.
 *
 * The Studio tab is dead unless the selected key's GROUP serves the modality —
 * image (Vertex/gemini), seedream-image (VolcEngine/newapi), and video models
 * can live on different platform groups, so a single key rarely serves all
 * three. The historical bootstrap grabbed `trial`/`keys[0]` blind to modality,
 * which on prod routinely landed on an antigravity key with no image models
 * (the "当前分组暂无可用的图片模型" dead-end). This makes the choice modality-aware:
 *
 *  1. keep `currentId` when it already serves the modality (respect the user's
 *     explicit selection, and keep the e2e's imagen-serving default stable);
 *  2. else prefer a serving key — `trial` first, then the first serving key;
 *  3. else fall back to `currentId`, then the global `trial`/first key, so the
 *     UI still has a selection and shows the honest empty state.
 */
export function pickModalityKey(
  options: readonly ModalityKeyOption[],
  modality: PickerModality,
  currentId: number | null,
  catalogBilling: CatalogBillingIndex
): number | null {
  if (options.length === 0) return currentId
  const serving = options.filter((o) => groupServes(modality, o.availableIds, catalogBilling))
  if (currentId != null && serving.some((o) => o.id === currentId)) return currentId
  const pickServing = serving.find((o) => o.isTrial) ?? serving[0]
  if (pickServing) return pickServing.id
  if (currentId != null) return currentId
  const fallback = options.find((o) => o.isTrial) ?? options[0]
  return fallback ? fallback.id : null
}

/**
 * Image aspect ratios are MODEL-SPECIFIC and sent transparently — no opaque
 * "landscape/portrait" wrapper hiding a fixed pixel size. Each model declares the
 * exact ratios its UPSTREAM accepts, and the option's `value` is the literal
 * `size` string put on the wire. We do NOT invent sizes the upstream rejects:
 *
 *  - Imagen (Vertex/gemini adaptor, ConvertImageRequest): the openai-compat `size`
 *    is mapped to imagen `aspectRatio`, and a `size` already containing ":" passes
 *    straight through. Imagen ONLY accepts 1:1, 3:4, 4:3, 9:16, 16:9 — the old
 *    1536x1024 / 1024x1536 presets mapped to 3:2 / 2:3, which Imagen hard-400s
 *    ("Invalid aspect ratio, 3:2"). The adaptor's WxH switch can't even PRODUCE
 *    4:3 / 3:4, so the only way to offer Imagen's full set is to send the ratio
 *    code verbatim. (ref: Google Imagen docs — supported aspectRatio set.)
 *  - Seedream 4.0 (VolcEngine ARK, openai-compat passthrough): `size` is a PIXEL
 *    "WxH" (or a 1K/2K/4K tier), NOT a ratio string — total pixels in
 *    [1024x1024, 4096x4096], ratio range [1/16, 16]. So Seedream's options carry
 *    the same ratio LABELS but a pixel `value`. (ref: VolcEngine doubao-seedream-4.0.)
 *
 * The chip shows the ratio (transparent); when `value` differs from the ratio
 * (Seedream pixels) the exact wire size is shown as a subtext.
 */
export interface ImageSizeOption {
  /** Aspect-ratio label shown on the chip, e.g. "16:9". */
  ratio: string
  /** EXACT string sent as the request `size` (ratio code for Imagen, WxH for Seedream). */
  value: string
}

/** Imagen: send the ratio code verbatim — the adaptor maps it to `aspectRatio`. */
export const IMAGEN_IMAGE_SIZES: ImageSizeOption[] = [
  { ratio: '1:1', value: '1:1' },
  { ratio: '3:4', value: '3:4' },
  { ratio: '4:3', value: '4:3' },
  { ratio: '9:16', value: '9:16' },
  { ratio: '16:9', value: '16:9' },
]

/**
 * Seedream: ARK wants pixels, not a ratio string. Same ratio labels, pixel values
 * at the 2K tier (maxEdge ≤ 2048 ⇒ "2K"), all within ARK's documented range.
 */
export const SEEDREAM_IMAGE_SIZES: ImageSizeOption[] = [
  { ratio: '1:1', value: '2048x2048' },
  { ratio: '3:4', value: '1536x2048' },
  { ratio: '4:3', value: '2048x1536' },
  { ratio: '9:16', value: '1152x2048' },
  { ratio: '16:9', value: '2048x1152' },
]

/**
 * Gemini-native image: send the ratio code verbatim — it rides extra_body.google.
 * image_config.aspect_ratio and the antigravity transform emits it as generationConfig.
 * imageConfig.aspectRatio to cloudcode-pa. A prod canary (2026-06-17) confirmed upstream
 * honors all 10 documented ratios (returned dims match each requested ratio within ~1%),
 * which is why R-001's "no picker" deferral is now lifted. Value === ratio (no pixel size:
 * gemini bills flat per image, so sentSize feeds aspect_ratio only). (ref: Google Gemini-3
 * image docs — supported aspectRatio set.)
 */
export const GEMINI_IMAGE_SIZES: ImageSizeOption[] = [
  { ratio: '1:1', value: '1:1' },
  { ratio: '2:3', value: '2:3' },
  { ratio: '3:2', value: '3:2' },
  { ratio: '3:4', value: '3:4' },
  { ratio: '4:3', value: '4:3' },
  { ratio: '4:5', value: '4:5' },
  { ratio: '5:4', value: '5:4' },
  { ratio: '9:16', value: '9:16' },
  { ratio: '16:9', value: '16:9' },
  { ratio: '21:9', value: '21:9' },
]

/** Video aspect ratios — passthrough hint to the task adaptor (TK does not interpret). */
export interface VideoAspectPreset {
  id: string
  label: string
}

export const VIDEO_ASPECT_PRESETS: VideoAspectPreset[] = [
  { id: '16:9', label: '16:9' },
  { id: '9:16', label: '9:16' },
  { id: '1:1', label: '1:1' },
]

/**
 * Fallback video duration default (seconds) used ONLY before a model is selected
 * or for a video model that declares no `videoDurations`. Real durations are
 * per-model and discrete (see MediaModelPresentation.videoDurations) — the global 1–60s
 * slider was a footgun: it let users request (and get quoted for) durations the
 * model's UPSTREAM always rejects, e.g. a 53s Veo clip @ $0.60/s = $31.80 that
 * Vertex hard-fails (Veo accepts only 4/6/8s). The backend still clamps to
 * [1,60] defensively, but the UI now never offers an out-of-range value.
 */
export const VIDEO_DURATION_DEFAULT = 8

/** Image count bounds for the n stepper. */
export const IMAGE_N_MIN = 1
export const IMAGE_N_MAX = 4

/* ────────────────────────────────────────────────────────────────────────────
 * Presentation overlay (NOT membership SSOT — catalog billing_mode is).
 *
 * Friendly names, badges, aspect ratios, discrete video durations, and verified
 * adaptor params. Runtime can build conservative defaults for future/private
 * catalog rows; preflight requires repo-known public servable media to be
 * explicitly curated here.
 * ──────────────────────────────────────────────────────────────────────────── */

/**
 * Advanced params the Studio can surface. Each is gated by a model's
 * `supportedParams` capability list so we NEVER render a control the selected
 * model's UPSTREAM ADAPTOR silently ignores ("real & transparent"). Membership
 * is verified against the new-api task/image adaptor request-builders, NOT
 * assumed — e.g. imagen/seedream honor none; seedance drops negative_prompt;
 * fps is honored by no adaptor (removed).
 */
export type StudioParam =
  | 'negativePrompt' // veo only: VeoParameters.NegativePrompt (gemini task adaptor)
  | 'seed' // veo + seedance: VeoParameters.Seed / doubao requestPayload.Seed
  | 'firstFrameImage' // veo + seedance: image-to-video first frame (sent as `image`)
  | 'generateAudio' // veo: metadata.generateAudio; seedance: metadata.generate_audio

export type QualityBadge = 'draft' | 'standard' | 'ultra' | 'fast' | 'cinematic'

export interface MediaModelPresentation {
  /** EXACT id sent to the gateway = billing key. Never substitute the display name. */
  modelId: string
  /** Friendly display name, e.g. "Imagen 4 · Ultra" (frontend-derived; no backend name). */
  displayName: string
  /** Quality badge shown ON the card (label, not the selection axis). */
  qualityBadge: QualityBadge
  /** i18n key for the badge text. */
  qualityBadgeKey: string
  /** Display-only vendor footnote, e.g. "Google Vertex" / "VolcEngine". */
  vendorLabel: string
  modality: StudioModality
  /**
   * Advanced params whose upstream adaptor ACTUALLY reads them (capability map,
   * verified against new-api adaptor code). Empty = no advanced controls.
   */
  supportedParams: StudioParam[]
  /** Other ids that are the SAME model under a different name (dedup display). */
  aliasIds?: string[]
  /** True ⇒ never auto-select; render a hard "needs apikey account" warning. */
  needsApikeyAccount?: boolean
  /**
   * Image modality only: the aspect-ratio options this model's UPSTREAM accepts,
   * each carrying the exact `size` string to send. Imagen ⇒ ratio codes, Seedream
   * ⇒ pixel WxH, Gemini-native ⇒ ratio codes (see ImageSizeOption). Absent for video.
   */
  imageSizes?: ImageSizeOption[]
  /**
   * True ⇒ this model is served via /v1/chat/completions (gemini-native image), not
   * /v1/images/generations, and bills a FLAT output_cost_per_image (no 1K/2K/4K size
   * tier). The Studio routes it through chat and skips the size-tier cost multiplier.
   */
  flatImageBilling?: boolean
  /**
   * True ⇒ this model bills a FLAT official per-image price with NO 1K/2K/4K
   * size-tier multiplier (mirrors backend tkIsFlatPerImageModel: imagen is billed
   * at Google's flat official price; the 2K→×1.5 / 4K→×2 multiplier is dropped for
   * imagen). DECOUPLED from `flatImageBilling`, which additionally implies the
   * chat-routing / n=1 / image-input behaviors imagen must NOT inherit. The
   * computed `pricesFlat` ORs the two, so gemini-native need not also set this.
   */
  flatPricePerImage?: boolean
  /**
   * Video modality only: the DISCRETE durations (seconds) this model's UPSTREAM
   * accepts — same "declare exactly what the upstream takes" contract as
   * `imageSizes`/`supportedParams`. The Studio renders these as chips and never
   * lets the user pick (or get quoted for) an out-of-range value, so a request is
   * priced ∩ servable, never a 400/FAILURE-on-submit footgun. Per the upstream
   * task adaptors (new-api ResolveVeoDuration / doubao) durations are passed
   * through unvalidated, so the upstream's own accepted set is the only guard.
   * The default selected value is the MAX of this list (videoDurationDefault).
   */
  videoDurations?: number[]
}

/** Presentation-only entries keyed by canonical model_id. */
export const MEDIA_MODEL_PRESENTATIONS: MediaModelPresentation[] = [
  // ── image (imagen/seedream honor NO advanced params per adaptor) ──
  {
    modelId: 'imagen-4.0-fast-generate-001',
    displayName: 'Imagen 4 · Fast',
    qualityBadge: 'draft',
    qualityBadgeKey: 'studio.badge.draft',
    vendorLabel: VERTEX,
    modality: 'image',
    supportedParams: [],
    imageSizes: IMAGEN_IMAGE_SIZES,
    flatPricePerImage: true, // Imagen bills Google's flat official $/image (no size tier) — see backend tkIsFlatPerImageModel
  },
  {
    modelId: 'seedream-4-0-250828',
    aliasIds: ['doubao-seedream-4-0-250828'],
    displayName: 'Seedream 4.0',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VOLC,
    modality: 'image',
    supportedParams: [],
    imageSizes: SEEDREAM_IMAGE_SIZES,
  },
  {
    modelId: 'doubao-seedream-5-0-260128',
    displayName: 'Seedream 5.0',
    qualityBadge: 'ultra',
    qualityBadgeKey: 'studio.badge.ultra',
    vendorLabel: VOLC,
    modality: 'image',
    supportedParams: [],
    imageSizes: SEEDREAM_IMAGE_SIZES,
  },
  {
    modelId: 'doubao-seedream-4-5-251128',
    displayName: 'Seedream 4.5',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VOLC,
    modality: 'image',
    supportedParams: [],
    imageSizes: SEEDREAM_IMAGE_SIZES,
  },
  {
    modelId: 'imagen-4.0-generate-001',
    displayName: 'Imagen 4 · Standard',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VERTEX,
    modality: 'image',
    supportedParams: [],
    imageSizes: IMAGEN_IMAGE_SIZES,
    flatPricePerImage: true, // Imagen bills Google's flat official $/image (no size tier) — see backend tkIsFlatPerImageModel
  },
  {
    modelId: 'imagen-4.0-ultra-generate-001',
    displayName: 'Imagen 4 · Ultra',
    qualityBadge: 'ultra',
    qualityBadgeKey: 'studio.badge.ultra',
    vendorLabel: VERTEX,
    modality: 'image',
    supportedParams: [],
    imageSizes: IMAGEN_IMAGE_SIZES,
    flatPricePerImage: true, // Imagen bills Google's flat official $/image (no size tier) — see backend tkIsFlatPerImageModel
  },
  {
    modelId: 'grok-imagine-image',
    displayName: 'Grok Imagine · Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: XAI,
    modality: 'image',
    supportedParams: [],
    flatPricePerImage: true,
  },
  {
    modelId: 'grok-imagine-image-quality',
    displayName: 'Grok Imagine · Quality',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: XAI,
    modality: 'image',
    supportedParams: [],
    flatPricePerImage: true,
  },
  // ── gemini-native image (Nano Banana family) — served via /v1/chat/completions
  //    (responseModalities IMAGE), NOT /v1/images/generations. Flat per-image billing.
  {
    modelId: 'gemini-3.1-flash-image',
    aliasIds: ['gemini-3.1-flash-image-preview'],
    displayName: 'Gemini 3.1 Flash Image',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: GEMINI,
    modality: 'image',
    supportedParams: [],
    flatImageBilling: true,
    imageSizes: GEMINI_IMAGE_SIZES,
  },
  {
    modelId: 'gemini-2.5-flash-image',
    aliasIds: ['gemini-2.5-flash-image-preview'],
    displayName: 'Gemini 2.5 Flash Image',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: GEMINI,
    modality: 'image',
    supportedParams: [],
    flatImageBilling: true,
    imageSizes: GEMINI_IMAGE_SIZES,
  },
  {
    modelId: 'gemini-3-pro-image-preview',
    aliasIds: ['gemini-3-pro-image', 'nano-banana-pro-preview'],
    displayName: 'Nano Banana Pro (Gemini 3 Pro Image)',
    qualityBadge: 'ultra',
    qualityBadgeKey: 'studio.badge.ultra',
    vendorLabel: GEMINI,
    modality: 'image',
    supportedParams: [],
    flatImageBilling: true,
    imageSizes: GEMINI_IMAGE_SIZES,
  },
  // gpt-image-* is deliberately ABSENT: it needs a type=apikey OpenAI account
  // (OAuth subscriptions 502). If a future probe adds an apikey-backed group,
  // add it here with needsApikeyAccount: true.

  // ── video ──
  {
    modelId: 'veo-3.1-generate-001',
    displayName: 'Veo 3.1',
    qualityBadge: 'cinematic',
    qualityBadgeKey: 'studio.badge.cinematic',
    vendorLabel: VERTEX,
    modality: 'video',
    supportedParams: ['negativePrompt', 'seed', 'firstFrameImage', 'generateAudio'],
    videoDurations: [4, 6, 8],
  },
  {
    modelId: 'grok-imagine-video',
    displayName: 'Grok Imagine Video',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: XAI,
    modality: 'video',
    supportedParams: [],
    videoDurations: [5],
  },
  {
    modelId: 'doubao-seedance-1-0-pro-250528',
    aliasIds: ['seedance-1-0-pro-250528'],
    displayName: 'Seedance 1.0 · Pro',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VOLC,
    modality: 'video',
    // doubao adaptor reads Seed + first-frame image; it has NO NegativePrompt field.
    supportedParams: ['seed', 'firstFrameImage', 'generateAudio'],
    // Seedance 1.0 Pro: discrete 5s / 10s (Volcengine Ark, high confidence).
    videoDurations: [5, 10],
  },
  {
    modelId: 'doubao-seedance-1-0-pro-fast-251015',
    displayName: 'Seedance 1.0 · Pro Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage', 'generateAudio'],
    // Live Ark submit probe used the 5s minimum task shape; keep conservative
    // until the official duration table for this fast SKU is captured.
    videoDurations: [5],
  },
  {
    modelId: 'doubao-seedance-1-5-pro-251215',
    displayName: 'Seedance 1.5 · Pro',
    qualityBadge: 'cinematic',
    qualityBadgeKey: 'studio.badge.cinematic',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage', 'generateAudio'],
    // Local VolcEngine pricing capture only documents 5s examples for 1.5 Pro.
    videoDurations: [5],
  },
  {
    modelId: 'doubao-seedance-2-0-260128',
    displayName: 'Seedance 2.0',
    qualityBadge: 'cinematic',
    qualityBadgeKey: 'studio.badge.cinematic',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage', 'generateAudio'],
    // Local VolcEngine pricing capture only documents 5s output examples for 2.0.
    videoDurations: [5],
  },
  {
    modelId: 'doubao-seedance-2-0-fast-260128',
    displayName: 'Seedance 2.0 · Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage', 'generateAudio'],
    // Seedance 2.0 Fast: sources conflict (4/8/12 vs 2–15); we take the cited
    // fast-variant discrete set 4/8/12 — conservative (never offer a value the
    // upstream rejects). TODO: verify against canonical Volcengine Ark docs.
    videoDurations: [4, 8, 12],
  },
]

function lookupPresentation(modelId: string): MediaModelPresentation | undefined {
  const direct = MEDIA_MODEL_PRESENTATIONS.find((m) => m.modelId === modelId)
  if (direct) return direct
  return MEDIA_MODEL_PRESENTATIONS.find((m) => m.aliasIds?.includes(modelId))
}

function buildMediaPresentationForCatalogRow(
  servedId: string,
  modality: StudioModality,
  presentation: MediaModelPresentation | undefined,
  vendor?: string
): MediaModelPresentation {
  if (presentation) return presentation
  return {
    modelId: servedId,
    displayName: defaultDisplayName(servedId),
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: formatVendorLabel(vendor),
    modality,
    supportedParams: [],
    videoDurations: modality === 'video' ? [VIDEO_DURATION_DEFAULT] : undefined,
  }
}

/** Live per-model price from the user's pricing catalog (getMePricingCatalog). */
export interface MediaPrice {
  /** USD per image at the 1K base tier (image models). */
  perImage?: number
  /** USD per second (video models). */
  perSecond?: number
  /** From catalog billing_mode — membership SSOT for Studio. */
  billingMode?: StudioModality
  /** Raw vendor slug from the catalog row (e.g. xai, vertex_ai). */
  vendor?: string
}
export type MediaPriceMap = ReadonlyMap<string, MediaPrice>

export interface ResolvedMediaModel {
  presentation: MediaModelPresentation
  /** The concrete id present in availableIds (primary or an alias). Billing key. */
  servedId: string
  /** Live price for this model (from the catalog), per modality. */
  baseImagePrice?: number
  perSecond?: number
}

/**
 * Resolve the models the user can actually use for `modality`: shown only when
 * (a) its primary OR an alias id is in `availableIds` (servable) AND (b) the live
 * `priceMap` has a price for it (priced) — priced ∩ servable, never a footgun.
 * Sorted cheap → premium.
 */
export function resolveAvailableModels(
  modality: StudioModality,
  availableIds: ReadonlySet<string>,
  priceMap: MediaPriceMap
): ResolvedMediaModel[] {
  const out: ResolvedMediaModel[] = []
  const seenCanonical = new Set<string>()

  for (const servedId of availableIds) {
    const price = priceMap.get(servedId)
    if (!price) continue
    if (price.billingMode !== modality) continue

    const baseImagePrice = modality === 'image' ? price.perImage : undefined
    const perSecond = modality === 'video' ? price.perSecond : undefined
    if (baseImagePrice == null && perSecond == null) continue

    const presentation = lookupPresentation(servedId)
    const canonicalId = presentation?.modelId ?? servedId
    if (seenCanonical.has(canonicalId)) continue
    seenCanonical.add(canonicalId)

    const resolvedPresentation = buildMediaPresentationForCatalogRow(servedId, modality, presentation, price.vendor)
    out.push({ presentation: resolvedPresentation, servedId, baseImagePrice, perSecond })
  }

  out.sort((a, b) => (a.baseImagePrice ?? a.perSecond ?? 0) - (b.baseImagePrice ?? b.perSecond ?? 0))
  return out
}

/**
 * First model the Studio should auto-select for a modality: the cheapest served
 * model that is NOT a footgun (needsApikeyAccount). Null when none are servable.
 */
export function defaultModelId(models: readonly ResolvedMediaModel[]): string | null {
  const safe = models.find((r) => !r.presentation.needsApikeyAccount)
  return safe ? safe.presentation.modelId : null
}

/**
 * Default selected video duration for a model: the MAX of its accepted
 * durations (user directive — land on the longest valid clip). Falls back to
 * VIDEO_DURATION_DEFAULT when the model declares no `videoDurations`.
 */
export function videoDurationDefault(durations: readonly number[] | undefined): number {
  return durations && durations.length ? Math.max(...durations) : VIDEO_DURATION_DEFAULT
}

/**
 * Snap a target duration to the model's NEAREST accepted value (ties → the
 * larger). Used by the Bake-Off, where one shared duration is compared across
 * models with DISJOINT accepted sets (e.g. Veo 4/6/8 vs Seedance 5/10 share
 * none): each panel runs a value its own upstream accepts, never a footgun.
 */
export function snapVideoDuration(target: number, durations: readonly number[] | undefined): number {
  if (!durations || !durations.length) return target
  return durations.reduce((best, d) => {
    const dd = Math.abs(d - target)
    const db = Math.abs(best - target)
    return dd < db || (dd === db && d > best) ? d : best
  })
}

/** Advanced param bounds. */
export const SEED_MIN = 0
export const SEED_MAX = 2147483647
