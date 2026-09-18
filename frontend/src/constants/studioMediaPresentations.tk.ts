/**
 * TokenKey-only: Studio media presentation + resolver helpers.
 *
 * **Membership SSOT** (which models a Studio tab may list for a key) is the
 * key/group entitlement pool — `/v1/models` or capabilities, which project
 * account `model_mapping`. Modality is `modalityForModel` (aligned with gateway
 * intent predicates). Public `/pricing` is NOT a Studio membership gate.
 *
 * **Price** from me/public catalogs is enrichment for cost estimates only:
 * missing per-image / per-second must not hide a mapping-backed model.
 *
 * **This file** holds presentation-only metadata (display names, aspect ratios,
 * discrete video durations, verified adaptor params) and synthesizes defaults
 * when a served id has no curated row.
 */

import { modalityForModel } from '@/constants/playgroundMedia.tk'
import type { VideoPriceTier } from '@/utils/mediaCostEstimate.tk'

export type StudioModality = 'image' | 'video'

/**
 * The modality axis the Studio SHELL reasons about for key selection. Chat /
 * image / video all use the entitlement pool + `modalityForModel`. Bake-off
 * reports its active sub-modality to the shell so the selected key still tracks
 * image vs video like the dedicated tabs.
 */
export type PickerModality = StudioModality | 'chat'

const VERTEX = 'Google Vertex'
const VOLC = 'VolcEngine'
const GEMINI = 'Google Gemini'
const OPENAI = 'OpenAI'
const XAI = 'xAI'
const DASHSCOPE = 'Alibaba DashScope'

const VENDOR_LABELS: Record<string, string> = {
  xai: XAI,
  vertex_ai: VERTEX,
  volcengine: VOLC,
  google: GEMINI,
  gemini: GEMINI,
  openai: OPENAI,
  dashscope: DASHSCOPE,
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
 * True when the entitlement pool exposes at least one id classified as
 * `modality` via `modalityForModel` (gateway-aligned intent predicates).
 */
export function hasCatalogMediaModality(
  modality: StudioModality,
  availableIds: ReadonlySet<string>
): boolean {
  for (const id of availableIds) {
    if (modalityForModel(id) === modality) return true
  }
  return false
}

/**
 * Whether this group's entitlement pool serves `modality` for the SHELL key
 * picker. Chat / image / video all use the same owner: pool ids +
 * `modalityForModel`. Bake-off passes its active image/video sub-modality so
 * the shell keeps the selected key aligned with the child mode.
 */
export function groupServes(
  modality: PickerModality,
  availableIds: ReadonlySet<string>
): boolean {
  for (const id of availableIds) {
    if (modalityForModel(id) === modality) return true
  }
  return false
}

/** One selectable key, reduced to what the modality-aware picker needs. */
export interface ModalityKeyOption {
  id: number
  /** A key literally named "trial" is the historical default landing key. */
  isTrial: boolean
  /** Model ids exposed by this key's group, or universal entitlement ids. */
  availableIds: ReadonlySet<string>
  /** Explicit key capability truth. Omitted for direct keys that use legacy catalog inference. */
  servedModalities?: ReadonlySet<PickerModality>
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
  currentId: number | null
): number | null {
  if (options.length === 0) return currentId
  const serving = options.filter((o) =>
    o.servedModalities ? o.servedModalities.has(modality) : groupServes(modality, o.availableIds)
  )
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
 * Wan 2.7: OpenAI-compat size is WxH pixels; Ali adaptor rewrites `x` → `*`.
 * Official 2K recommended set (wan2.7-image max; Studio also uses this for pro —
 * 4K remains available via API for pro but is not a Studio chip yet).
 * Billing is flat per successful image (no Seedream-style size-tier multiplier).
 * ref: https://help.aliyun.com/zh/model-studio/text-to-image
 */
export const WAN27_IMAGE_SIZES: ImageSizeOption[] = [
  { ratio: '1:1', value: '2048x2048' },
  { ratio: '3:4', value: '1728x2368' },
  { ratio: '4:3', value: '2368x1728' },
  { ratio: '9:16', value: '1536x2688' },
  { ratio: '16:9', value: '2688x1536' },
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

/**
 * OpenAI gpt-image-* via /v1/images/generations: pixel sizes the Images API
 * accepts for the family (square + landscape/portrait). Used when a mapping-
 * backed gpt-image id has no curated presentation row.
 */
export const GPT_IMAGE_SIZES: ImageSizeOption[] = [
  { ratio: '1:1', value: '1024x1024' },
  { ratio: '3:2', value: '1536x1024' },
  { ratio: '2:3', value: '1024x1536' },
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
 * Presentation overlay (NOT membership SSOT — pool + modalityForModel is).
 *
 * Friendly names, badges, aspect ratios, discrete video durations, and verified
 * adaptor params. Runtime synthesizes defaults for mapping-backed ids without a
 * curated row; preflight still requires repo-known public media to be curated.
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
   * lets the user pick (or get quoted for) an out-of-range value, so the quoted
   * duration stays in the upstream-accepted set. Per the upstream
   * task adaptors (new-api ResolveVeoDuration / doubao) durations are passed
   * through unvalidated, so the upstream's own accepted set is the only guard.
   * The default selected value is the MAX of this list (videoDurationDefault).
   */
  videoDurations?: number[]
}

/** Presentation-only entries keyed by canonical model_id. */
export const MEDIA_MODEL_PRESENTATIONS: MediaModelPresentation[] = [
  // ── image (seedream honor NO advanced params per adaptor) ──
  // Imagen rows removed: Google image surface converged to gemini-3-pro-image /
  // gemini-3.1-flash-image (Vertex video remains veo-3.1-generate-001 only).
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
    modelId: 'doubao-seedream-5.0-lite',
    displayName: 'Seedream 5.0 Lite',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
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
    modelId: 'wan2.7-image',
    displayName: 'Wan 2.7',
    qualityBadge: 'draft',
    qualityBadgeKey: 'studio.badge.draft',
    vendorLabel: DASHSCOPE,
    modality: 'image',
    supportedParams: [],
    imageSizes: WAN27_IMAGE_SIZES,
    flatPricePerImage: true, // Ali Token Plan bills flat CNY/image ÷ FX — see backend tkIsFlatPerImageModel
  },
  {
    modelId: 'wan2.7-image-pro',
    displayName: 'Wan 2.7 · Pro',
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: DASHSCOPE,
    modality: 'image',
    supportedParams: [],
    imageSizes: WAN27_IMAGE_SIZES,
    flatPricePerImage: true,
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
    aliasIds: ['gemini-3.1-flash-image-preview', 'nano-2'],
    displayName: 'Nano Banana 2 (Gemini 3.1 Flash Image)',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: GEMINI,
    modality: 'image',
    supportedParams: [],
    flatImageBilling: true,
    imageSizes: GEMINI_IMAGE_SIZES,
  },
  {
    modelId: 'gemini-3-pro-image',
    // Antigravity OAuth remaps Pro → 3.1-flash-image (true Pro wire id 404,
    // 2026-09-17 us4). nano-pro stays as the marketing alias for that path;
    // newapi/TokenSea can still serve true gemini-3-pro-image.
    aliasIds: ['gemini-3-pro-image-preview', 'nano-banana-pro-preview', 'nano-pro'],
    displayName: 'Nano Banana Pro (Gemini 3 Pro Image)',
    qualityBadge: 'ultra',
    qualityBadgeKey: 'studio.badge.ultra',
    vendorLabel: GEMINI,
    modality: 'image',
    supportedParams: [],
    flatImageBilling: true,
    imageSizes: GEMINI_IMAGE_SIZES,
  },
  // gpt-image-* membership comes from account model_mapping /v1/models — no
  // curated presentation required. buildMediaPresentationForCatalogRow supplies
  // GPT_IMAGE_SIZES defaults when a gpt-image id is served. Edge OpenAI OAuth
  // verified gpt-image-2.5-flare/sunburst servable_image_generated (2026-09-17).

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
  {
    modelId: 'doubao-seedance-2-5-260628',
    displayName: 'Seedance 2.5',
    qualityBadge: 'cinematic',
    qualityBadgeKey: 'studio.badge.cinematic',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage', 'generateAudio'],
    // Served through XRToken (ARK-compatible reseller, channel_type=54); billed
    // on the Ark id above. The captured Volcengine Ark price table documents
    // only 480p/720p output for this SKU and cites 5s examples, so keep the
    // conservative single duration until an official table is captured.
    videoDurations: [5],
  },
  {
    modelId: 'doubao-seedance-2.0-mini',
    displayName: 'Seedance 2.0 · Mini',
    qualityBadge: 'fast',
    qualityBadgeKey: 'studio.badge.fast',
    vendorLabel: VOLC,
    modality: 'video',
    supportedParams: ['seed', 'firstFrameImage', 'generateAudio'],
    // Same XRToken path as Seedance 2.5. Official Ark id is dotted
    // (doubao-seedance-2.0-mini), not the hyphen+date form used by its siblings.
    // Price table documents 480p/720p only; 5s kept conservative.
    videoDurations: [5],
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
  const synthesized: MediaModelPresentation = {
    modelId: servedId,
    displayName: defaultDisplayName(servedId),
    qualityBadge: 'standard',
    qualityBadgeKey: 'studio.badge.standard',
    vendorLabel: formatVendorLabel(vendor) || (servedId.startsWith('gpt-image-') ? 'OpenAI' : ''),
    modality,
    supportedParams: [],
    videoDurations: modality === 'video' ? [VIDEO_DURATION_DEFAULT] : undefined,
  }
  if (modality === 'image' && servedId.startsWith('gpt-image-')) {
    synthesized.imageSizes = GPT_IMAGE_SIZES
  }
  return synthesized
}

/** Live per-model price from the user's pricing catalog (getMePricingCatalog). */
export interface MediaPrice {
  /** USD per image at the 1K base tier (image models). */
  perImage?: number
  /** USD per second (video models). Minimum tier when videoTiers is set. */
  perSecond?: number
  /** Official resolution×audio ladder (from public /pricing). */
  videoTiers?: readonly VideoPriceTier[]
  /** Catalog billing_mode — price enrichment only, never a membership gate. */
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
  videoTiers?: readonly VideoPriceTier[]
}

/**
 * Resolve models the user can use for `modality` from the entitlement pool
 * (`availableIds` ← group account model_mapping / capabilities). Price map is
 * optional enrichment for estimates; missing live price must not hide a
 * mapping-backed id. Sorted priced-cheap → priced-premium → unpriced.
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
    if (modalityForModel(servedId) !== modality) continue

    const baseImagePrice = modality === 'image' ? price?.perImage : undefined
    const perSecond = modality === 'video' ? price?.perSecond : undefined

    const presentation = lookupPresentation(servedId)
    const canonicalId = presentation?.modelId ?? servedId
    if (seenCanonical.has(canonicalId)) continue
    seenCanonical.add(canonicalId)

    const resolvedPresentation = buildMediaPresentationForCatalogRow(
      servedId,
      modality,
      presentation,
      price?.vendor
    )
    out.push({
      presentation: resolvedPresentation,
      servedId,
      baseImagePrice,
      perSecond,
      videoTiers: price?.videoTiers,
    })
  }

  out.sort((a, b) => {
    const av = a.baseImagePrice ?? a.perSecond
    const bv = b.baseImagePrice ?? b.perSecond
    if (av == null && bv == null) return a.servedId.localeCompare(b.servedId)
    if (av == null) return 1
    if (bv == null) return -1
    return av - bv
  })
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
