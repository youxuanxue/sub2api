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
