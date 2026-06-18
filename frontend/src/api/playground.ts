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

/** A multimodal user-message content part (text or an image reference). */
type ChatContentPart =
  | { type: 'text'; text: string }
  | { type: 'image_url'; image_url: { url: string } }

/**
 * Build a user message: a plain string when there's no input image, else a
 * multimodal content array [text, image_url]. Livefire-verified that the gateway
 * forwards the image_url part to the gemini-native upstream (POST /v1/chat/completions).
 */
function userMessage(text: string, inputImage?: string): { role: 'user'; content: string | ChatContentPart[] } {
  if (inputImage) {
    return {
      role: 'user',
      content: [
        { type: 'text', text },
        { type: 'image_url', image_url: { url: inputImage } }
      ]
    }
  }
  return { role: 'user', content: text }
}

export interface GeminiImageViaChatRequest {
  model: string
  prompt: string
  /**
   * Aspect-ratio code (e.g. "16:9") → extra_body.google.image_config.aspect_ratio.
   * Degrades gracefully (model's default ratio) if an upstream strips it.
   */
  aspectRatio?: string
  /**
   * Optional INPUT image for image-to-image (图生图): a data: URI or http(s) URL.
   * When set, the user message becomes a multimodal content array carrying the
   * image alongside the refinement text — livefire-verified that the gateway
   * forwards the image_url part to the gemini-native upstream and returns an
   * edited image. Omit for plain text-to-image.
   */
  inputImage?: string
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
    messages: [userMessage(body.prompt, body.inputImage)],
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

export interface ImageToPromptRequest {
  /** A vision-capable chat model id (e.g. a gemini-*-flash served by the group). */
  model: string
  /** The image to describe: data: URI or http(s) URL. */
  image: string
  /** Optional instruction; a sensible default is used when omitted. */
  instruction?: string
}

/**
 * Reverse-prompt (逆向提示词): describe an image as a reusable generation prompt.
 * Sends the image to a vision-capable chat model and returns the assistant's
 * text. Same multimodal /v1/chat/completions path livefire-verified to forward
 * the input image; chat (not image) timeout since it returns text only.
 */
export async function gatewayImageToPrompt(
  apiKey: string,
  gatewayBaseUrl: string,
  body: ImageToPromptRequest,
  signal?: AbortSignal
): Promise<string> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/chat/completions`
  const instruction =
    body.instruction ||
    'Describe this image as a concise, vivid text-to-image generation prompt. Output only the prompt, no preamble.'
  const raw = await gatewayRequestJSON(
    apiKey,
    url,
    {
      method: 'POST',
      body: { model: body.model, messages: [userMessage(instruction, body.image)], stream: false },
      timeoutMs: PLAYGROUND_FETCH_TIMEOUT_MS
    },
    signal
  )
  return extractChatText(raw)
}

/** Pull the assistant message text out of a chat completion (string or parts). */
function extractChatText(resp: unknown): string {
  const root = resp && typeof resp === 'object' ? (resp as Record<string, unknown>) : null
  const choices = root?.choices
  if (!Array.isArray(choices) || !choices.length) return ''
  const message = (choices[0] as Record<string, unknown>)?.message as Record<string, unknown> | undefined
  const content = message?.content
  if (typeof content === 'string') return content.trim()
  if (Array.isArray(content)) {
    return content
      .map((p) => (p && typeof p === 'object' && typeof (p as { text?: unknown }).text === 'string' ? (p as { text: string }).text : ''))
      .join('')
      .trim()
  }
  return ''
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
  return gatewayRequestJSON(apiKey, url, { method: 'GET', timeoutMs: 30_000 }, signal)
}
