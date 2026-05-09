import type { AxiosError } from 'axios'

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

export function createNetworkError(url?: string): ApiNetworkError {
  return {
    status: 0,
    code: 'NETWORK_ERROR',
    message: NETWORK_ERROR_MESSAGE,
    offline: isBrowserOffline(),
    ...(url ? { url } : {})
  }
}

export function networkErrorFromAxios(error: AxiosError): ApiNetworkError {
  return createNetworkError(error.config?.url)
}
