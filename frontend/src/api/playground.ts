/**
 * Browser-side calls to the API gateway (/v1/*) using the user's API key.
 * Playground uses fetch (not apiClient) so JWT from axios does not leak into gateway.
 */

export interface GatewayModelEntry {
  id: string
  type?: string
  display_name?: string
  created_at?: string
}

export interface GatewayModelsResponse {
  object?: string
  data: GatewayModelEntry[]
}

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant'
  content: string
}

export interface ChatCompletionRequest {
  model: string
  messages: ChatMessage[]
  temperature?: number
  max_tokens?: number
}

/** Default max output tokens per request (cost guardrail). */
export const PLAYGROUND_DEFAULT_MAX_TOKENS = 1024
/** Hard cap aligned with approved prototype footnote. */
export const PLAYGROUND_MAX_TOKENS_CAP = 4096
/** Max user+assistant turns kept in browser memory. */
export const PLAYGROUND_MAX_TURNS = 50
export const PLAYGROUND_FETCH_TIMEOUT_MS = 60_000
/**
 * Video-task FETCH timeout. Must be far larger than the chat/submit timeout: a
 * Veo terminal SUCCESS response carries the generated clip as INLINE base64
 * (response.videos[0].bytesBase64Encoded, 10–20 MB) which takes ~30s to stream
 * back through the gateway. The old 30s ceiling aborted exactly on that body,
 * and because the backend deletes the registry record on terminal status, the
 * retry then 404'd → the card showed a false "failed — refunded" for a video
 * that actually generated (and was billed). 180s gives the large body ample
 * headroom; processing polls return in ms and are unaffected.
 */
export const PLAYGROUND_VIDEO_FETCH_TIMEOUT_MS = 180_000

function stripTrailingSlashes(u: string): string {
  return u.replace(/\/+$/, '')
}

/**
 * Base URL for OpenAI-compatible gateway paths (…/v1/chat/completions).
 * Prefer site `api_base_url` when set; else same origin.
 */
export function resolveGatewayBaseUrl(apiBaseFromSettings: string | undefined): string {
  const raw = (apiBaseFromSettings || '').trim()
  if (raw) {
    return stripTrailingSlashes(raw)
  }
  if (typeof window !== 'undefined') {
    return stripTrailingSlashes(window.location.origin)
  }
  return ''
}

export async function gatewayListModels(
  apiKey: string,
  gatewayBaseUrl: string,
  signal?: AbortSignal
): Promise<GatewayModelsResponse> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/models`
  const res = await fetch(url, {
    method: 'GET',
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: 'application/json'
    },
    signal
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json() as Promise<GatewayModelsResponse>
}

/** Shared gateway JSON call with per-request timeout + caller abort linkage. */
async function gatewayRequestJSON(
  apiKey: string,
  url: string,
  init: { method: 'GET' | 'POST'; body?: unknown; timeoutMs: number },
  signal?: AbortSignal
): Promise<unknown> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), init.timeoutMs)
  const onAbort = (): void => {
    ctrl.abort()
  }
  if (signal) {
    if (signal.aborted) {
      ctrl.abort()
    } else {
      signal.addEventListener('abort', onAbort, { once: true })
    }
  }
  try {
    const res = await fetch(url, {
      method: init.method,
      headers: {
        Authorization: `Bearer ${apiKey}`,
        Accept: 'application/json',
        ...(init.body !== undefined ? { 'Content-Type': 'application/json' } : {})
      },
      ...(init.body !== undefined ? { body: JSON.stringify(init.body) } : {}),
      signal: ctrl.signal
    })
    // Read the body once as text: a second res.text() after res.json() would
    // always throw (stream already consumed), losing plain-text error bodies.
    const text = await res.text().catch(() => '')
    let json: unknown = null
    try {
      json = JSON.parse(text)
    } catch {
      json = null
    }
    if (!res.ok) {
      const msg =
        json && typeof json === 'object' && 'error' in json
          ? JSON.stringify((json as { error?: unknown }).error)
          : text
      throw new Error(msg || `HTTP ${res.status}`)
    }
    return json
  } finally {
    clearTimeout(timer)
    if (signal) {
      signal.removeEventListener('abort', onAbort)
    }
  }
}

export async function gatewayChatCompletion(
  apiKey: string,
  gatewayBaseUrl: string,
  body: ChatCompletionRequest,
  signal?: AbortSignal
): Promise<unknown> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/chat/completions`
  return gatewayRequestJSON(
    apiKey,
    url,
    {
      method: 'POST',
      body: {
        model: body.model,
        messages: body.messages,
        temperature: body.temperature,
        max_tokens: body.max_tokens,
        stream: false
      },
      timeoutMs: PLAYGROUND_FETCH_TIMEOUT_MS
    },
    signal
  )
}

export interface ImageGenerationRequest {
  model: string
  prompt: string
  /** omit for upstream default */
  size?: string
  /** number of images (1-4); backend bills per delivered image. Omit → upstream default (1). */
  n?: number
}

/** Image generation can run well past a minute upstream. */
export const PLAYGROUND_IMAGE_TIMEOUT_MS = 180_000

export async function gatewayImageGenerations(
  apiKey: string,
  gatewayBaseUrl: string,
  body: ImageGenerationRequest,
  signal?: AbortSignal
): Promise<unknown> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/images/generations`
  const payload: Record<string, unknown> = { model: body.model, prompt: body.prompt }
  if (body.size) payload.size = body.size
  if (body.n && body.n > 0) payload.n = body.n
  return gatewayRequestJSON(
    apiKey,
    url,
    { method: 'POST', body: payload, timeoutMs: PLAYGROUND_IMAGE_TIMEOUT_MS },
    signal
  )
}

export interface GeminiImageViaChatRequest {
  model: string
  prompt: string
  /**
   * Aspect-ratio code (e.g. "16:9") → extra_body.google.image_config.aspect_ratio.
   * Degrades gracefully (model's default ratio) if an upstream strips it.
   */
  aspectRatio?: string
}

/**
 * Gemini-native image generation rides /v1/chat/completions (NOT /v1/images/
 * generations, NOT the /v1beta native route — that one is gemini-platform-group only
 * and 400s for antigravity/newapi groups). Verified against prod: the response is a
 * regular OpenAI chat completion whose choices[].message.content carries the image as
 * `![image](data:image/…;base64,…)` markdown (new-api / antigravity both convert the
 * gemini inline image to this shape). Parse with extractChatImageItems. This is the
 * universal, platform-adaptive path. Uses the image timeout (gen can exceed a minute).
 */
export async function gatewayGeminiImageViaChat(
  apiKey: string,
  gatewayBaseUrl: string,
  body: GeminiImageViaChatRequest,
  signal?: AbortSignal
): Promise<unknown> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/chat/completions`
  const payload: Record<string, unknown> = {
    model: body.model,
    messages: [{ role: 'user', content: body.prompt }],
    stream: false
  }
  if (body.aspectRatio) {
    payload.extra_body = { google: { image_config: { aspect_ratio: body.aspectRatio } } }
  }
  return gatewayRequestJSON(
    apiKey,
    url,
    { method: 'POST', body: payload, timeoutMs: PLAYGROUND_IMAGE_TIMEOUT_MS },
    signal
  )
}

export interface VideoGenerationRequest {
  model: string
  prompt: string
  /** seconds; gateway default is 8 when omitted */
  duration?: number
  /**
   * Optional framing hint forwarded verbatim to the task adaptor (e.g. "16:9").
   * Omit to use the model's own default — the proven zero-extra-field path. The
   * gateway passes the body through; the adaptor decides whether to honor it.
   */
  aspectRatio?: string
  /**
   * Advanced (optional, only sent when set). seed/negativePrompt ride the
   * upstream `metadata.*` catch-all (read by the veo/doubao adaptors); `image` is
   * a first-frame image-to-video reference sent top-level. We never send
   * `video_url` — the backend rejects video input as unpriced.
   */
  seed?: number
  negativePrompt?: string
  image?: string
}

export async function gatewayVideoSubmit(
  apiKey: string,
  gatewayBaseUrl: string,
  body: VideoGenerationRequest,
  signal?: AbortSignal
): Promise<unknown> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/video/generations`
  const payload: Record<string, unknown> = { model: body.model, prompt: body.prompt }
  if (body.duration) payload.duration = body.duration
  if (body.aspectRatio) payload.aspect_ratio = body.aspectRatio
  if (body.image) payload.image = body.image
  const metadata: Record<string, unknown> = {}
  if (typeof body.seed === 'number') metadata.seed = body.seed
  if (body.negativePrompt) metadata.negative_prompt = body.negativePrompt
  if (Object.keys(metadata).length > 0) payload.metadata = metadata
  return gatewayRequestJSON(
    apiKey,
    url,
    { method: 'POST', body: payload, timeoutMs: PLAYGROUND_FETCH_TIMEOUT_MS },
    signal
  )
}

export async function gatewayVideoFetch(
  apiKey: string,
  gatewayBaseUrl: string,
  taskId: string,
  signal?: AbortSignal
): Promise<unknown> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/video/generations/${encodeURIComponent(taskId)}`
  // 180s, not 30s: the terminal SUCCESS body is a 10–20 MB inline base64 clip
  // (see PLAYGROUND_VIDEO_FETCH_TIMEOUT_MS) — a short timeout aborts mid-download
  // and the poll then mis-reports the (actually generated) video as failed.
  return gatewayRequestJSON(apiKey, url, { method: 'GET', timeoutMs: PLAYGROUND_VIDEO_FETCH_TIMEOUT_MS }, signal)
}
