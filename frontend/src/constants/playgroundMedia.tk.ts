// TokenKey-only playground media support: model-modality classification and
// tolerant parsing of image/video gateway responses.
import { buildDataVideoUri } from '@/utils/studioInlineVideo.tk'
//
// Why a frontend table: GET /v1/models returns bare ids (gateway_handler.go
// writeModelsList) and the public pricing catalog carries no image/video
// capability tag (pricing_catalog_tk.go buildCapabilities), so the playground
// must classify locally. The patterns mirror what the backend actually serves:
//   - image  — `gpt-image-` prefix is the backend's own intent predicate
//              (service/openai_images.go isOpenAIImageGenerationModel);
//              imagen-* (Vertex) and *seedream* (Doubao) are the media families
//              priced in tk_pricing_overlay.json.
//   - video  — veo-* (Vertex) and *seedance* (Doubao Seedance) are the families
//              served via /v1/video/generations (task adaptor channel types 45/54).
//   - image (gemini-native) — gemini-*-image / nano-banana ("Nano Banana") models
//              output images, but via /v1/chat/completions (responseModalities
//              IMAGE), NOT /v1/images/generations. The predicate mirrors the
//              backend isImageGenerationModel() family (antigravity_gateway_service.go).
// Unknown ids stay 'chat' — wrong-modality submits get a clear gateway error,
// never a silent misroute.

export type PlaygroundModality = 'chat' | 'image' | 'video'

/**
 * Gemini-native image-generation ids: `gemini…-image`, `gemini…-image-preview`,
 * `gemini…-image-<variant>`, and the `nano-banana` family. Must NOT match plain
 * gemini chat ids (e.g. gemini-2.5-flash, gemini-3-flash-agent) — only ids whose
 * name carries an `-image` segment. Mirrors backend isImageGenerationModel().
 */
const GEMINI_NATIVE_IMAGE_RE = /(?:^|\/)gemini[-\w.]*-image(?:-[-\w.]*)?$/

export function isGeminiNativeImageModel(modelId: string): boolean {
  const id = (modelId || '').trim().toLowerCase()
  return GEMINI_NATIVE_IMAGE_RE.test(id) || id.includes('nano-banana')
}

export function modalityForModel(modelId: string): PlaygroundModality {
  const id = (modelId || '').trim().toLowerCase()
  if (!id) return 'chat'
  if (id.includes('seedance') || id.startsWith('veo-')) return 'video'
  if (
    id.startsWith('gpt-image-') ||
    id.startsWith('imagen-') ||
    id.includes('seedream') ||
    isGeminiNativeImageModel(id)
  )
    return 'image'
  return 'chat'
}

/**
 * Pick a vision-capable gemini CHAT model from the group's available ids, for
 * reverse-prompt (图→prompt). Gemini chat models are natively multimodal (they
 * read input images — livefire-verified). Prefer the cheapest (flash-lite >
 * flash > the rest); exclude image/video-generation ids. Returns '' if none.
 */
export function pickVisionChatModel(availableIds: Iterable<string>): string {
  const gemini: string[] = []
  for (const raw of availableIds) {
    const id = (raw || '').trim()
    if (!id) continue
    if (id.toLowerCase().startsWith('gemini') && modalityForModel(id) === 'chat') gemini.push(id)
  }
  if (!gemini.length) return ''
  const rank = (id: string): number => {
    const l = id.toLowerCase()
    if (l.includes('flash-lite')) return 0
    if (l.includes('flash')) return 1
    if (l.includes('pro')) return 2
    return 3
  }
  return gemini.sort((a, b) => rank(a) - rank(b))[0]
}

/** One generated image, normalized from data[].url / data[].b64_json. */
export interface PlaygroundImageItem {
  /** http(s) URL or data: URI, ready for an <img> src */
  src: string
  revisedPrompt?: string
  /**
   * S3 key when the gateway offloaded this image (data[].s3_key). The `src` is a
   * short-lived presigned URL; the key lets a reloaded Studio session re-mint a
   * fresh URL via POST /v1/images/presign without re-generating. Absent for
   * inline data: URI images (gemini-native chat path) — those never expire.
   */
  s3Key?: string
}

function asRecord(v: unknown): Record<string, unknown> | null {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
}

/**
 * Cap on the length of an inline base64 string we will turn into a `data:` URI.
 * The bytes arrive from upstream verbatim; a hostile/compromised relay could
 * return a multi-hundred-MB string that freezes the tab when the browser decodes
 * it into an <img>/<video :src>. ~48 MB decoded (64M base64 chars) is far above
 * any legitimate short clip / generated image yet bounds the worst case. Over the
 * cap (or empty) → treated as "no media", so the UI shows the raw JSON details
 * instead of hanging.
 */
const MAX_INLINE_B64_CHARS = 64 * 1024 * 1024

function withinInlineB64Cap(b64: string): boolean {
  return b64.length > 0 && b64.length <= MAX_INLINE_B64_CHARS
}

/**
 * Normalize an images-generations response (upstream JSON passed through by
 * bridge.RunImageRelay): {data:[{url} | {b64_json, revised_prompt?}]}.
 */
export function extractImageItems(resp: unknown): PlaygroundImageItem[] {
  const root = asRecord(resp)
  const data = root?.data
  if (!Array.isArray(data)) return []
  const items: PlaygroundImageItem[] = []
  for (const entry of data) {
    const rec = asRecord(entry)
    if (!rec) continue
    const revised = typeof rec.revised_prompt === 'string' ? rec.revised_prompt : undefined
    // s3_key is the gateway's offload marker (see backend openai_images_s3_tk.go):
    // the url is then a short-lived presigned link the Studio re-mints on reload.
    const s3Key = typeof rec.s3_key === 'string' && rec.s3_key ? rec.s3_key : undefined
    // http(s) only — the src lands in <a :href>/<img :src>, so a hostile
    // upstream payload must not smuggle javascript:/data: schemes (the video
    // path applies the same guard in extractVideoUrl).
    if (typeof rec.url === 'string' && /^https?:\/\//i.test(rec.url)) {
      items.push({ src: rec.url, revisedPrompt: revised, s3Key })
    } else if (typeof rec.b64_json === 'string' && withinInlineB64Cap(rec.b64_json)) {
      items.push({ src: `data:image/png;base64,${rec.b64_json}`, revisedPrompt: revised })
    }
  }
  return items
}

/**
 * Extract image(s) from a CHAT COMPLETION response — gemini-native image models
 * (Nano Banana family) are served via /v1/chat/completions (verified against prod:
 * antigravity + newapi groups both return the image, NOT the /v1beta native route
 * which is gemini-platform-group only). The image arrives as markdown in
 * choices[].message.content: `![image](data:image/jpeg;base64,…)`. Some upstreams
 * may instead return structured content parts (image_url / inline_data); handle both.
 * Reuses extractImageItems' scheme guard + decoded-size cap so a hostile relay can't
 * smuggle a javascript: scheme or a tab-freezing multi-hundred-MB b64.
 */
export function extractChatImageItems(resp: unknown): PlaygroundImageItem[] {
  const root = asRecord(resp)
  const choices = root?.choices
  if (!Array.isArray(choices)) return []
  const items: PlaygroundImageItem[] = []
  const pushDataUri = (uri: string): void => {
    const m = /^data:image\/[\w.+-]+;base64,([A-Za-z0-9+/=]+)$/i.exec(uri)
    if (m && withinInlineB64Cap(m[1])) items.push({ src: uri })
  }
  const pushUrl = (url: string): void => {
    if (/^https?:\/\//i.test(url)) items.push({ src: url })
    else pushDataUri(url)
  }
  const pushMarkdownImagesFromText = (text: string): void => {
    const dataRe = /\(\s*(data:image\/[\w.+-]+;base64,[A-Za-z0-9+/=]+)\s*\)/gi
    let mm: RegExpExecArray | null
    while ((mm = dataRe.exec(text)) !== null) pushDataUri(mm[1])
    const httpRe = /!\[[^\]]*\]\(\s*(https?:\/\/[^\s)]+)\s*\)/gi
    while ((mm = httpRe.exec(text)) !== null) pushUrl(mm[1])
  }
  const pushAnthropicImagePart = (part: Record<string, unknown>): void => {
    const source = asRecord(part.source)
    if (!source || source.type !== 'base64') return
    const data = typeof source.data === 'string' ? source.data : ''
    if (!withinInlineB64Cap(data)) return
    const mime =
      typeof source.media_type === 'string' && source.media_type.startsWith('image')
        ? source.media_type
        : 'image/png'
    items.push({ src: `data:${mime};base64,${data}` })
  }
  for (const choice of choices) {
    const message = asRecord(asRecord(choice)?.message)
    if (!message) continue
    const content = message.content
    if (typeof content === 'string') {
      pushMarkdownImagesFromText(content)
    } else if (Array.isArray(content)) {
      for (const part of content) {
        const p = asRecord(part)
        if (!p) continue
        if (typeof p.text === 'string') {
          pushMarkdownImagesFromText(p.text)
        }
        if (p.type === 'image') {
          pushAnthropicImagePart(p)
        }
        const imageUrl = asRecord(p.image_url)
        if (typeof imageUrl?.url === 'string') pushUrl(imageUrl.url)
        else if (typeof p.image_url === 'string') pushUrl(p.image_url)
        const inline = asRecord(p.inline_data) ?? asRecord(p.inlineData)
        if (inline && typeof inline.data === 'string') {
          const mime =
            typeof inline.mime_type === 'string'
              ? inline.mime_type
              : typeof inline.mimeType === 'string'
                ? inline.mimeType
                : 'image/png'
          if (mime.startsWith('image') && withinInlineB64Cap(inline.data)) {
            items.push({ src: `data:${mime};base64,${inline.data}` })
          }
        }
      }
    }
  }
  return items
}

/** Submit response is {"id":"vt_…"} (openai_gateway_tk_video.go); tolerate task_id too. */
export function extractVideoTaskId(resp: unknown): string {
  const root = asRecord(resp)
  if (!root) return ''
  if (typeof root.id === 'string' && root.id) return root.id
  if (typeof root.task_id === 'string' && root.task_id) return root.task_id
  return ''
}

export type PlaygroundVideoState = 'processing' | 'succeeded' | 'failed'

/**
 * Map the fetch response to a terminal/non-terminal state. The body is the
 * upstream task JSON verbatim (vendor-specific), so two shapes must be handled:
 *
 *  - Vertex / Gemini long-running OPERATION: {done:bool, error?:{message}, response}
 *    (new-api relay/channel/task/vertex ParseTaskResult). No `status` string —
 *    done+error are the signal. This is what veo-3.1 returns.
 *  - Volcengine / doubao / OpenAI-video STRING status: {status:"succeeded"|…}.
 *
 * Same terminal vocabulary the backend uses to expire its task record.
 */
export function videoStateFromFetch(resp: unknown): PlaygroundVideoState {
  const root = asRecord(resp)
  if (!root) return 'processing'
  // Vertex/Gemini operation shape.
  if (typeof root.done === 'boolean') {
    const err = asRecord(root.error)
    if (err && typeof err.message === 'string' && err.message.trim()) return 'failed'
    return root.done ? 'succeeded' : 'processing'
  }
  // String-status shape (volcengine/doubao + OpenAI-video "completed").
  const status = typeof root.status === 'string' ? root.status.toLowerCase() : ''
  if (status === 'success' || status === 'succeeded' || status === 'completed') return 'succeeded'
  if (status === 'failure' || status === 'failed' || status === 'error') return 'failed'
  return 'processing'
}

/**
 * The fetch response body is the upstream task JSON verbatim (vendor-specific),
 * so the video URL has no single canonical path. Handle:
 *
 *  - Vertex / Gemini operation: response.videos[0].bytesBase64Encoded (or
 *    response.bytesBase64Encoded / response.video) — base64-inline video that we
 *    wrap into a `data:video/…;base64,…` URI (veo returns the bytes, not a URL).
 *  - Volcengine ark: content.video_url; generic data.video_url / video_url / data.url.
 *  - Last resort: a bounded deep scan for the first http(s) string under a
 *    video-ish key or with a video file extension.
 *
 * http(s) is anchored/case-insensitive and inline bytes are limited to
 * `data:video/…` so a hostile payload cannot smuggle a javascript:/data:text
 * scheme into <video :src> / <a :href>. Empty string → the UI shows the raw
 * JSON details instead of a broken player.
 */
export function extractVideoUrl(resp: unknown): string {
  const root = asRecord(resp)
  if (!root) return ''

  // Vertex/Gemini operation: inline base64 video bytes.
  const response = asRecord(root.response)
  if (response) {
    const gen = asRecord(response.generateVideoResponse)
    const generated = gen?.generatedVideos
    if (Array.isArray(generated) && generated.length) {
      const v0 = asRecord(asRecord(generated[0])?.video)
      const uri = v0 && typeof v0.uri === 'string' ? v0.uri.trim() : ''
      if (/^https?:\/\//i.test(uri)) return uri
    }
    const videos = response.videos
    if (Array.isArray(videos) && videos.length) {
      const v0 = asRecord(videos[0])
      const b64 = v0 && typeof v0.bytesBase64Encoded === 'string' ? v0.bytesBase64Encoded : ''
      if (withinInlineB64Cap(b64)) {
        const claimed = v0 && typeof v0.mimeType === 'string' ? v0.mimeType : ''
        const encoding = v0 && typeof v0.encoding === 'string' ? v0.encoding : ''
        return buildDataVideoUri(claimed, b64, encoding)
      }
    }
    for (const key of ['bytesBase64Encoded', 'video']) {
      const b = response[key]
      if (typeof b === 'string' && withinInlineB64Cap(b)) {
        const encoding = typeof response.encoding === 'string' ? response.encoding : ''
        return buildDataVideoUri('video/mp4', b, encoding)
      }
    }
  }

  const content = asRecord(root.content)
  const data = asRecord(root.data)
  for (const v of [content?.video_url, data?.video_url, root.video_url, data?.url]) {
    // http(s) only, anchored + case-insensitive — same guard as extractImageItems.
    if (typeof v === 'string' && /^https?:\/\//i.test(v)) return v
  }
  return deepScanVideoUrl(root, 0)
}

const DEEP_SCAN_MAX_DEPTH = 4

function deepScanVideoUrl(node: unknown, depth: number): string {
  if (depth > DEEP_SCAN_MAX_DEPTH) return ''
  if (Array.isArray(node)) {
    for (const item of node) {
      const hit = deepScanVideoUrl(item, depth + 1)
      if (hit) return hit
    }
    return ''
  }
  const rec = asRecord(node)
  if (!rec) return ''
  for (const [key, value] of Object.entries(rec)) {
    if (typeof value === 'string' && /^https?:\/\//i.test(value)) {
      const k = key.toLowerCase()
      if (k.includes('video') || /\.(mp4|webm|mov)(\?|$)/i.test(value)) return value
    }
  }
  for (const value of Object.values(rec)) {
    const hit = deepScanVideoUrl(value, depth + 1)
    if (hit) return hit
  }
  return ''
}
