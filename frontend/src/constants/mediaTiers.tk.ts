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

export interface MediaTierCandidate {
  /** Exact model id sent to the gateway and used as the billing key. */
  modelId: string
  /** Display-only vendor label, e.g. "Google Vertex" / "VolcEngine". */
  vendorLabel: string
  /** USD per image at the 1K base tier (image tiers only). */
  baseImagePrice?: number
  /** USD per second (video tiers only). */
  perSecond?: number
}

export interface MediaTier {
  id: string
  modality: StudioModality
  /** i18n key for the tier name, e.g. studio.tier.image.draft.label */
  labelKey: string
  /** i18n key for the one-line tagline. */
  taglineKey: string
  /** i18n key for the pre-filled "known-good" prompt (never-blank rule). */
  samplePromptKey: string
  candidates: MediaTierCandidate[]
}

const VERTEX = 'Google Vertex'
const VOLC = 'VolcEngine'

/**
 * Image tiers. Prices: imagen-4 fast/std/ultra = $0.02/$0.04/$0.06;
 * seedream-4 = $0.0298507 (tk_pricing_overlay.json, verified 2026-06-13).
 */
export const IMAGE_TIERS: MediaTier[] = [
  {
    id: 'draft',
    modality: 'image',
    labelKey: 'studio.tiers.image.draft.label',
    taglineKey: 'studio.tiers.image.draft.tagline',
    samplePromptKey: 'studio.tiers.image.draft.sample',
    candidates: [
      { modelId: 'imagen-4.0-fast-generate-001', vendorLabel: VERTEX, baseImagePrice: 0.02 },
      { modelId: 'seedream-4-0-250828', vendorLabel: VOLC, baseImagePrice: 0.029850746268656716 },
      { modelId: 'doubao-seedream-4-0-250828', vendorLabel: VOLC, baseImagePrice: 0.029850746268656716 },
    ],
  },
  {
    id: 'standard',
    modality: 'image',
    labelKey: 'studio.tiers.image.standard.label',
    taglineKey: 'studio.tiers.image.standard.tagline',
    samplePromptKey: 'studio.tiers.image.standard.sample',
    candidates: [{ modelId: 'imagen-4.0-generate-001', vendorLabel: VERTEX, baseImagePrice: 0.04 }],
  },
  {
    id: 'ultra',
    modality: 'image',
    labelKey: 'studio.tiers.image.ultra.label',
    taglineKey: 'studio.tiers.image.ultra.tagline',
    samplePromptKey: 'studio.tiers.image.ultra.sample',
    candidates: [{ modelId: 'imagen-4.0-ultra-generate-001', vendorLabel: VERTEX, baseImagePrice: 0.06 }],
  },
]

/**
 * Video tiers. Prices: veo-3.1 = $0.40/s, veo-3.1-fast = $0.15/s,
 * seedance-1.0-pro = $0.1088/s, seedance-2.0-fast = $0.1194/s
 * (tk_pricing_overlay.json output_cost_per_second, verified 2026-06-13).
 */
export const VIDEO_TIERS: MediaTier[] = [
  {
    id: 'fast',
    modality: 'video',
    labelKey: 'studio.tiers.video.fast.label',
    taglineKey: 'studio.tiers.video.fast.tagline',
    samplePromptKey: 'studio.tiers.video.fast.sample',
    candidates: [
      { modelId: 'veo-3.1-fast-generate-001', vendorLabel: VERTEX, perSecond: 0.15 },
      { modelId: 'doubao-seedance-2-0-fast-260128', vendorLabel: VOLC, perSecond: 0.11940298507462686 },
    ],
  },
  {
    id: 'standard',
    modality: 'video',
    labelKey: 'studio.tiers.video.standard.label',
    taglineKey: 'studio.tiers.video.standard.tagline',
    samplePromptKey: 'studio.tiers.video.standard.sample',
    candidates: [
      { modelId: 'seedance-1-0-pro-250528', vendorLabel: VOLC, perSecond: 0.10880597014925374 },
      { modelId: 'doubao-seedance-1-0-pro-250528', vendorLabel: VOLC, perSecond: 0.10880597014925374 },
    ],
  },
  {
    id: 'cinematic',
    modality: 'video',
    labelKey: 'studio.tiers.video.cinematic.label',
    taglineKey: 'studio.tiers.video.cinematic.tagline',
    samplePromptKey: 'studio.tiers.video.cinematic.sample',
    candidates: [{ modelId: 'veo-3.1-generate-001', vendorLabel: VERTEX, perSecond: 0.4 }],
  },
]

export interface ResolvedTier {
  tier: MediaTier
  candidate: MediaTierCandidate
}

/**
 * Resolve a tier against the set of available (priced+servable) model ids from
 * the user's key group. Returns the first candidate present in `availableIds`,
 * or null when the tier has no backing model for this key (so the UI hides it).
 */
export function resolveTier(tier: MediaTier, availableIds: ReadonlySet<string>): ResolvedTier | null {
  for (const candidate of tier.candidates) {
    if (availableIds.has(candidate.modelId)) {
      return { tier, candidate }
    }
  }
  return null
}

/** Resolve all tiers for a modality; drops tiers with no available candidate. */
export function resolveAvailableTiers(
  modality: StudioModality,
  availableIds: ReadonlySet<string>
): ResolvedTier[] {
  const tiers = modality === 'image' ? IMAGE_TIERS : VIDEO_TIERS
  const out: ResolvedTier[] = []
  for (const tier of tiers) {
    const resolved = resolveTier(tier, availableIds)
    if (resolved) out.push(resolved)
  }
  return out
}

/**
 * True when this model pool backs at least one tier of the modality — i.e. the
 * Studio tab is actually usable for a key whose group exposes `availableIds`.
 */
export function modalityHasTiers(
  modality: StudioModality,
  availableIds: ReadonlySet<string>
): boolean {
  return resolveAvailableTiers(modality, availableIds).length > 0
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
 * `supportedParams` capability list so we NEVER render a control that the
 * selected model silently ignores (the "real & transparent" rule).
 */
export type StudioParam =
  | 'quality' // image: enum standard|hd (native channels) — cosmetic, no TK price delta
  | 'style' // image: vivid|natural (DALL-E family) — passthrough
  | 'negativePrompt' // image+video: forwarded via Extra / metadata.negative_prompt
  | 'seed' // image+video: forwarded via Extra / metadata.seed
  | 'firstFrameImage' // video: image-to-video first frame (sent as `image`)
  | 'fps' // video: metadata.fps (seedance honors)
  | 'resolution' // video: metadata.resolution per-channel

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
  /** USD per image at the 1K base tier (image only). */
  baseImagePrice?: number
  /** USD per second (video only). */
  perSecond?: number
  /** Advanced params that ACTUALLY take effect for this model (capability map). */
  supportedParams: StudioParam[]
  /** Other ids that are the SAME model under a different name (dedup display). */
  aliasIds?: string[]
  /** True ⇒ never auto-select; render a hard "needs apikey account" warning. */
  needsApikeyAccount?: boolean
}

/**
 * Catalog of priced+servable media models, authored cheap → premium. Prices are
 * verified against backend/internal/service/tk_pricing_overlay.json and mirror
 * the same per-unit numbers the tier candidates used. The `availableIds ∩`
 * filter in resolveAvailableModels() hides any a given key-group can't serve.
 */
export const MEDIA_MODELS: MediaModel[] = [
  // ── image (cheap → premium) ──
  {
    modelId: 'imagen-4.0-fast-generate-001',
    displayName: 'Imagen 4 · Fast',
    qualityBadge: 'draft',
    qualityBadgeKey: 'studio.badge.draft',
    vendorLabel: VERTEX,
    modality: 'image',
    baseImagePrice: 0.02,
    supportedParams: ['seed', 'negativePrompt'],
  },
  {
    modelId: 'seedream-4-0-250828',
    aliasIds: ['doubao-seedream-4-0-250828'],
    displayName: 'Seedream 4.0',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VOLC,
    modality: 'image',
    baseImagePrice: 0.029850746268656716,
    supportedParams: ['seed', 'negativePrompt'],
  },
  {
    modelId: 'imagen-4.0-generate-001',
    displayName: 'Imagen 4 · Standard',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VERTEX,
    modality: 'image',
    baseImagePrice: 0.04,
    supportedParams: ['seed', 'negativePrompt'],
  },
  {
    modelId: 'imagen-4.0-ultra-generate-001',
    displayName: 'Imagen 4 · Ultra',
    qualityBadge: 'ultra',
    qualityBadgeKey: 'studio.badge.ultra',
    vendorLabel: VERTEX,
    modality: 'image',
    baseImagePrice: 0.06,
    supportedParams: ['seed', 'negativePrompt'],
  },
  // gpt-image-* is deliberately ABSENT: it needs a type=apikey OpenAI account
  // (OAuth subscriptions 502). If a future probe adds an apikey-backed group,
  // add it here with needsApikeyAccount: true.

  // ── video (cheap → premium) ──
  {
    modelId: 'seedance-1-0-pro-250528',
    aliasIds: ['doubao-seedance-1-0-pro-250528'],
    displayName: 'Seedance 1.0 · Pro',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: VOLC,
    modality: 'video',
    perSecond: 0.10880597014925374,
    supportedParams: ['seed', 'negativePrompt', 'fps', 'firstFrameImage'],
  },
  {
    modelId: 'doubao-seedance-2-0-fast-260128',
    displayName: 'Seedance 2.0 · Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VOLC,
    modality: 'video',
    perSecond: 0.11940298507462686,
    supportedParams: ['seed', 'negativePrompt', 'fps', 'firstFrameImage'],
  },
  {
    modelId: 'veo-3.1-fast-generate-001',
    displayName: 'Veo 3.1 · Fast',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VERTEX,
    modality: 'video',
    perSecond: 0.15,
    supportedParams: ['negativePrompt', 'firstFrameImage'],
  },
  {
    modelId: 'veo-3.1-generate-001',
    displayName: 'Veo 3.1 · Cinematic',
    qualityBadge: 'cinematic',
    qualityBadgeKey: 'studio.badge.cinematic',
    vendorLabel: VERTEX,
    modality: 'video',
    perSecond: 0.4,
    supportedParams: ['negativePrompt', 'firstFrameImage'],
  },
]

export interface ResolvedModel {
  model: MediaModel
  /** The concrete id present in availableIds (primary or an alias). Billing key. */
  servedId: string
}

/**
 * Resolve the models the user can actually use for `modality`: a model is shown
 * only when its primary OR an alias id is in `availableIds` (priced ∩ servable).
 * Sorted cheap → premium. Mirrors resolveTier's filter at model granularity.
 */
export function resolveAvailableModels(
  modality: StudioModality,
  availableIds: ReadonlySet<string>
): ResolvedModel[] {
  const out: ResolvedModel[] = []
  for (const model of MEDIA_MODELS) {
    if (model.modality !== modality) continue
    const ids = [model.modelId, ...(model.aliasIds ?? [])]
    const servedId = ids.find((id) => availableIds.has(id))
    if (servedId) out.push({ model, servedId })
  }
  out.sort(
    (a, b) =>
      (a.model.baseImagePrice ?? a.model.perSecond ?? 0) -
      (b.model.baseImagePrice ?? b.model.perSecond ?? 0)
  )
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

/** Advanced param options + recommended defaults (smooth: hit Generate works). */
export const IMAGE_QUALITY_OPTIONS = ['standard', 'hd'] as const
export const IMAGE_STYLE_OPTIONS = ['vivid', 'natural'] as const
export const SEED_MIN = 0
export const SEED_MAX = 2147483647
export const VIDEO_FPS_OPTIONS = [16, 24] as const
export const VIDEO_FPS_DEFAULT = 24
