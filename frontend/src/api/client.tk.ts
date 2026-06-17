import type { AxiosError } from 'axios'

// The axios response interceptor (client.ts) used to reject with a *plain object*
// { status, code, message, ... }. Because a plain object is not an Error, every
// `e instanceof Error ? e.message : String(e)` call site across the app fell through
// to String(e) === "[object Object]" — the bug fixed at the source here. ApiError is
// a real Error subclass carrying the same flattened fields, so `instanceof Error`
// holds everywhere and `.message` surfaces the backend message without per-call-site
// helpers. Object.assign makes every custom field an *enumerable own* property; the
// constructor additionally redefines `message` as enumerable (Error's constructor sets
// it non-enumerable), so spreads / JSON.stringify retain the full shape too — strictly
// more robust than the old plain object. Direct field access (e.message / e.status /
// e.reason) is unaffected by enumerability, so existing consumers (utils/apiError.ts,
// utils/authError.ts, the 409 mixed_channel checks) keep working unchanged.
export interface ApiErrorFields {
  status?: number
  code?: number | string
  message?: string
  reason?: string
  error?: string
  metadata?: Record<string, unknown>
  offline?: boolean
  url?: string
}

export class ApiError extends Error {
  status?: number
  code?: number | string
  reason?: string
  error?: string
  metadata?: Record<string, unknown>
  offline?: boolean
  url?: string

  constructor(fields: ApiErrorFields) {
    super(fields.message ?? 'API error')
    this.name = 'ApiError'
    Object.assign(this, fields)
    // Error's constructor installs `message` as a non-enumerable own property, which
    // assignment via Object.assign preserves. Redefine it enumerable so a spread or
    // JSON.stringify of this error retains `message` alongside the other fields.
    Object.defineProperty(this, 'message', {
      value: this.message,
      enumerable: true,
      writable: true,
      configurable: true,
    })
  }
}

export function createApiError(fields: ApiErrorFields): ApiError {
  return new ApiError(fields)
}

const NETWORK_ERROR_MESSAGE = 'Network error. Please check your connection.'
const NETWORK_ERROR_CODES = new Set([
  'ERR_NETWORK',
  'ERR_INTERNET_DISCONNECTED',
  'ERR_NETWORK_CHANGED',
  'ERR_CONNECTION_RESET',
  'ECONNABORTED'
])

export interface ApiNetworkError {
  status: 0
  code: 'NETWORK_ERROR'
  message: string
  offline: boolean
  url?: string
}

export function isBrowserOffline(): boolean {
  return typeof navigator !== 'undefined' && 'onLine' in navigator && !navigator.onLine
}

export function isNetworkError(error: unknown): error is ApiNetworkError {
  if (!error || typeof error !== 'object') return false
  const candidate = error as { status?: unknown; code?: unknown; response?: unknown }
  if (candidate.status === 0) return true
  if (candidate.response) return false
  return typeof candidate.code === 'string' && NETWORK_ERROR_CODES.has(candidate.code)
}

export function requestUrlFromError(error: unknown): string | undefined {
  if (!error || typeof error !== 'object') return undefined
  const directUrl = (error as { url?: unknown }).url
  if (typeof directUrl === 'string') return directUrl
  const config = (error as { config?: { url?: unknown } }).config
  return typeof config?.url === 'string' ? config.url : undefined
}

// Returns an ApiError instance (not a plain object) so a network failure is also a
// real Error — same rationale as ApiError above. The status:0 / code:'NETWORK_ERROR'
// fields are preserved, so the isNetworkError duck-type guard still matches it.
export function createNetworkError(url?: string): ApiError {
  return new ApiError({
    status: 0,
    code: 'NETWORK_ERROR',
    message: NETWORK_ERROR_MESSAGE,
    offline: isBrowserOffline(),
    ...(url ? { url } : {})
  })
}

export function networkErrorFromAxios(error: AxiosError): ApiError {
  return createNetworkError(error.config?.url)
}
