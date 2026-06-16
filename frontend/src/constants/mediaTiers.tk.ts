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

export type StudioModality = 'image' | 'video'

const VERTEX = 'Google Vertex'
const VOLC = 'VolcEngine'

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
  modality: StudioModality,
  currentId: number | null
): number | null {
  if (options.length === 0) return currentId
  const serving = options.filter((o) => modalityHasTiers(modality, o.availableIds))
  if (currentId != null && serving.some((o) => o.id === currentId)) return currentId
  const pickServing = serving.find((o) => o.isTrial) ?? serving[0]
  if (pickServing) return pickServing.id
  if (currentId != null) return currentId
  const fallback = options.find((o) => o.isTrial) ?? options[0]
  return fallback ? fallback.id : null
}

/**
 * Image aspect/size presets. We send only sizes the current gateway path is
 * proven to accept (the existing playground set), and surface each preset's
 * CLASSIFIED billing tier + multiplier (mirrored client-side) so pricing stays
 * transparent without inventing fragile upstream sizes (god-view deviation from
 * the mockup's literal 1K/2K/4K ladder — see design-delta).
 */
export interface ImageAspectPreset {
  id: string
  labelKey: string
  /** WxH string sent as the request `size`. */
  size: string
}

export const IMAGE_ASPECT_PRESETS: ImageAspectPreset[] = [
  { id: 'square', labelKey: 'studio.image.aspect.square', size: '1024x1024' },
  { id: 'landscape', labelKey: 'studio.image.aspect.landscape', size: '1536x1024' },
  { id: 'portrait', labelKey: 'studio.image.aspect.portrait', size: '1024x1536' },
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

/** Video duration bounds (handlers clamp to [1,60]; default 8s). */
export const VIDEO_DURATION_MIN = 1
export const VIDEO_DURATION_MAX = 60
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
  },
  {
    modelId: 'imagen-4.0-generate-001',
    displayName: 'Imagen 4 · Standard',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VERTEX,
    modality: 'image',
    supportedParams: [],
  },
  {
    modelId: 'imagen-4.0-ultra-generate-001',
    displayName: 'Imagen 4 · Ultra',
    qualityBadge: 'ultra',
    qualityBadgeKey: 'studio.badge.ultra',
    vendorLabel: VERTEX,
    modality: 'image',
    supportedParams: [],
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
  },
  {
    modelId: 'doubao-seedance-2-0-fast-260128',
    displayName: 'Seedance 2.0 · Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage'],
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
  },
  {
    modelId: 'veo-3.1-generate-001',
    displayName: 'Veo 3.1 · Cinematic',
    qualityBadge: 'cinematic',
    qualityBadgeKey: 'studio.badge.cinematic',
    vendorLabel: VERTEX,
    modality: 'video',
    supportedParams: ['negativePrompt', 'seed', 'firstFrameImage'],
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

/** Advanced param bounds. */
export const SEED_MIN = 0
export const SEED_MAX = 2147483647
