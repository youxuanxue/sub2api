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
