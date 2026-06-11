// TokenKey-only playground media support: model-modality classification and
// tolerant parsing of image/video gateway responses.
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
// Unknown ids stay 'chat' — wrong-modality submits get a clear gateway error,
// never a silent misroute.

export type PlaygroundModality = 'chat' | 'image' | 'video'

export function modalityForModel(modelId: string): PlaygroundModality {
  const id = (modelId || '').trim().toLowerCase()
  if (!id) return 'chat'
  if (id.includes('seedance') || id.startsWith('veo-')) return 'video'
  if (id.startsWith('gpt-image-') || id.startsWith('imagen-') || id.includes('seedream')) return 'image'
  return 'chat'
}

/** One generated image, normalized from data[].url / data[].b64_json. */
export interface PlaygroundImageItem {
  /** http(s) URL or data: URI, ready for an <img> src */
  src: string
  revisedPrompt?: string
}

function asRecord(v: unknown): Record<string, unknown> | null {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
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
    if (typeof rec.url === 'string' && rec.url) {
      items.push({ src: rec.url, revisedPrompt: revised })
    } else if (typeof rec.b64_json === 'string' && rec.b64_json) {
      items.push({ src: `data:image/png;base64,${rec.b64_json}`, revisedPrompt: revised })
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
 * Map the fetch response's status to a terminal/non-terminal state, same
 * vocabulary the backend uses to expire its task record (video_relay.go
 * ParseTaskResult: success/succeeded and failure/failed are terminal).
 */
export function videoStateFromFetch(resp: unknown): PlaygroundVideoState {
  const root = asRecord(resp)
  const status = typeof root?.status === 'string' ? root.status.toLowerCase() : ''
  if (status === 'success' || status === 'succeeded') return 'succeeded'
  if (status === 'failure' || status === 'failed') return 'failed'
  return 'processing'
}

/**
 * The fetch response body is the upstream task JSON verbatim (vendor-specific),
 * so the video URL has no single canonical path. Check the shapes we know
 * (Volc ark content.video_url, generic data.video_url / video_url / data.url),
 * then fall back to a bounded deep scan for the first http(s) string under a
 * video-ish key or with a video file extension. Empty string when nothing
 * matches — the UI then shows the raw JSON instead of a broken player.
 */
export function extractVideoUrl(resp: unknown): string {
  const root = asRecord(resp)
  if (!root) return ''
  const content = asRecord(root.content)
  const data = asRecord(root.data)
  for (const v of [content?.video_url, data?.video_url, root.video_url, data?.url]) {
    if (typeof v === 'string' && v.startsWith('http')) return v
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
    if (typeof value === 'string' && value.startsWith('http')) {
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
