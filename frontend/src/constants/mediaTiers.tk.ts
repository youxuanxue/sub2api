/**
 * TokenKey-only: media (image/video) quality-tier catalog for the Studio.
 *
 * The Studio picks MODELS by a human "quality tier × price" card, never by a
 * bare model id — the vendor (Vertex / VolcEngine / …) is a footnote, not a
 * choice (design: docs/playground-media-redesign.md §3.2/§4.1).
 *
 * Each tier carries ORDERED candidate models. resolveTier() picks the first
 * candidate that is BOTH (a) priced+servable here and (b) present in the user's
 * key-group model pool (GET /v1/models). So a tier lights up only when a real,
 * priced model backs it — never a 400-on-submit footgun.
 *
 * Prices are verified against backend/internal/service/tk_pricing_overlay.json
 * (output_cost_per_image / output_cost_per_second). They drive the client cost
 * MIRROR (utils/mediaCostEstimate.tk.ts); the backend pre-flight hold remains
 * the source of truth and corrects any drift.
 */

import { modalityForModel } from '@/constants/playgroundMedia.tk'

export type StudioModality = 'image' | 'video'

/**
 * The modality axis the Studio SHELL reasons about for key selection. Chat is a
 * peer Studio tab (folded in from the retired /playground), but it has no media
 * tier catalog — a key "serves chat" when its /v1/models pool exposes any
 * chat-classified id (modalityForModel). image/video keep the media-tier check.
 * Bake-off passes no picker modality (dual sub-modality → user owns the key).
 */
export type PickerModality = StudioModality | 'chat'

const VERTEX = 'Google Vertex'
const VOLC = 'VolcEngine'
const GEMINI = 'Google Gemini'

/**
 * True when this group's pool backs at least one media model of the modality —
 * i.e. the Studio tab is usable for a key whose group exposes `availableIds`.
 * Drives the modality-aware key picker (pickModalityKey), which runs across ALL
 * the user's groups, so it is PRICE-AGNOSTIC by design: it must not require a
 * per-group price-catalog fetch. /v1/models is already filtered to priced models
 * upstream, so servable here implies priced for the per-key card resolver.
 */
export function modalityHasTiers(
  modality: StudioModality,
  availableIds: ReadonlySet<string>
): boolean {
  return MEDIA_MODELS.some(
    (m) =>
      m.modality === modality &&
      [m.modelId, ...(m.aliasIds ?? [])].some((id) => availableIds.has(id))
  )
}

/**
 * Whether this group's pool serves `modality` for the SHELL's key picker.
 * Dispatches chat (any chat-classified id in the pool) vs media (tier catalog),
 * so the modality-aware landing/annotation logic treats chat as a first-class
 * peer of image/video without a media tier entry.
 */
export function groupServes(
  modality: PickerModality,
  availableIds: ReadonlySet<string>
): boolean {
  if (modality === 'chat') {
    for (const id of availableIds) {
      if (modalityForModel(id) === 'chat') return true
    }
    return false
  }
  return modalityHasTiers(modality, availableIds)
}

/** One selectable key, reduced to what the modality-aware picker needs. */
export interface ModalityKeyOption {
  id: number
  /** A key literally named "trial" is the historical default landing key. */
  isTrial: boolean
  /** Model ids exposed by this key's group (its GET /v1/models pool). */
  availableIds: ReadonlySet<string>
}

/**
 * Pick the key the Studio should land on for `modality`.
 *
 * The Studio tab is dead unless the selected key's GROUP serves the modality —
 * image (Vertex/gemini), seedream-image (VolcEngine/newapi) and the video tiers
 * each live on a different platform group, so a single key rarely serves all
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
  currentId: number | null
): number | null {
  if (options.length === 0) return currentId
  const serving = options.filter((o) => groupServes(modality, o.availableIds))
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
 * per-model and discrete (see MediaModel.videoDurations) — the global 1–60s
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
 * Model catalog (transparent model picker)
 *
 * The Studio now lets the user pick the ACTUAL model — shown humanely (friendly
 * name + price + vendor footnote + raw id subtext), not a bare quality tier.
 * This completes the originally-planned "Advanced model picker" (design §3.2)
 * that #769 left unshipped. The quality label becomes a card BADGE, not the
 * selection axis.
 *
 * Same hard rule as resolveTier: only models BOTH (a) priced+servable here and
 * (b) present in the user's key-group pool (GET /v1/models) are shown — never a
 * 400-on-submit footgun. The raw modelId stays the billing key; displayName is
 * cosmetic only.
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

export type QualityBadge = 'draft' | 'standard' | 'ultra' | 'fast' | 'cinematic'

export interface MediaModel {
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

/**
 * Static catalog of media models = display metadata + the verified capability
 * map ONLY. Prices are NOT hardcoded here — they come live from the per-user
 * pricing catalog (getMePricingCatalog, the same source #788 established), so a
 * price change in the overlay can't drift. A model is shown only when it is BOTH
 * in the key-group's GET /v1/models pool AND carries a live catalog price
 * (priced ∩ servable — never a 400-on-submit footgun). modelId stays the billing
 * key; displayName is cosmetic.
 */
export const MEDIA_MODELS: MediaModel[] = [
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
    modelId: 'seedance-1-0-pro-250528',
    aliasIds: ['doubao-seedance-1-0-pro-250528'],
    displayName: 'Seedance 1.0 · Pro',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VOLC,
    modality: 'video',
    // doubao adaptor reads Seed + first-frame image; it has NO NegativePrompt field.
    supportedParams: ['seed', 'firstFrameImage'],
    // Seedance 1.0 Pro: discrete 5s / 10s (Volcengine Ark, high confidence).
    videoDurations: [5, 10],
  },
  {
    modelId: 'doubao-seedance-2-0-fast-260128',
    displayName: 'Seedance 2.0 · Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage'],
    // Seedance 2.0 Fast: sources conflict (4/8/12 vs 2–15); we take the cited
    // fast-variant discrete set 4/8/12 — conservative (never offer a value the
    // upstream rejects). TODO: verify against canonical Volcengine Ark docs.
    videoDurations: [4, 8, 12],
  },
  {
    modelId: 'veo-3.1-fast-generate-001',
    displayName: 'Veo 3.1 · Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VERTEX,
    modality: 'video',
    // VeoParameters honors NegativePrompt + Seed; first-frame image supported.
    supportedParams: ['negativePrompt', 'seed', 'firstFrameImage'],
    // Veo 3.1: discrete 4/6/8s (Vertex AI official, high confidence). With a
    // first-frame/reference image upstream only returns 8s — not modeled here.
    videoDurations: [4, 6, 8],
  },
  {
    modelId: 'veo-3.1-generate-001',
    displayName: 'Veo 3.1 · Cinematic',
    qualityBadge: 'cinematic',
    qualityBadgeKey: 'studio.badge.cinematic',
    vendorLabel: VERTEX,
    modality: 'video',
    supportedParams: ['negativePrompt', 'seed', 'firstFrameImage'],
    // Veo 3.1: discrete 4/6/8s (Vertex AI official, high confidence).
    videoDurations: [4, 6, 8],
  },
]

/** Live per-model price from the user's pricing catalog (getMePricingCatalog). */
export interface MediaPrice {
  /** USD per image at the 1K base tier (image models). */
  perImage?: number
  /** USD per second (video models). */
  perSecond?: number
}
export type MediaPriceMap = ReadonlyMap<string, MediaPrice>

export interface ResolvedModel {
  model: MediaModel
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
): ResolvedModel[] {
  const out: ResolvedModel[] = []
  for (const model of MEDIA_MODELS) {
    if (model.modality !== modality) continue
    const ids = [model.modelId, ...(model.aliasIds ?? [])]
    const servedId = ids.find((id) => availableIds.has(id))
    if (!servedId) continue
    const price = ids.map((id) => priceMap.get(id)).find((p) => p != null)
    const baseImagePrice = modality === 'image' ? price?.perImage : undefined
    const perSecond = modality === 'video' ? price?.perSecond : undefined
    if (baseImagePrice == null && perSecond == null) continue // no live price → hide
    out.push({ model, servedId, baseImagePrice, perSecond })
  }
  out.sort((a, b) => (a.baseImagePrice ?? a.perSecond ?? 0) - (b.baseImagePrice ?? b.perSecond ?? 0))
  return out
}

/**
 * First model the Studio should auto-select for a modality: the cheapest served
 * model that is NOT a footgun (needsApikeyAccount). Null when none are servable.
 */
export function defaultModelId(models: readonly ResolvedModel[]): string | null {
  const safe = models.find((r) => !r.model.needsApikeyAccount)
  return safe ? safe.model.modelId : null
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
