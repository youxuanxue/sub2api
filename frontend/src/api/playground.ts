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

export async function gatewayChatCompletion(
  apiKey: string,
  gatewayBaseUrl: string,
  body: ChatCompletionRequest,
  signal?: AbortSignal
): Promise<unknown> {
  const url = `${stripTrailingSlashes(gatewayBaseUrl)}/v1/chat/completions`
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), PLAYGROUND_FETCH_TIMEOUT_MS)
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
      method: 'POST',
      headers: {
        Authorization: `Bearer ${apiKey}`,
        Accept: 'application/json',
        'Content-Type': 'application/json'
      },
      body: JSON.stringify({
        model: body.model,
        messages: body.messages,
        temperature: body.temperature,
        max_tokens: body.max_tokens,
        stream: false
      }),
      signal: ctrl.signal
    })
    const json = await res.json().catch(() => null)
    if (!res.ok) {
      const msg =
        json && typeof json === 'object' && json !== null && 'error' in json
          ? JSON.stringify((json as { error?: unknown }).error)
          : await res.text().catch(() => '')
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
