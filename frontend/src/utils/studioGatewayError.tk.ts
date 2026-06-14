/**
 * TokenKey-only: classify a gateway error message (thrown by api/playground.ts
 * gatewayRequestJSON, where the message is the stringified upstream error object
 * or raw text) into a stable code the Studio maps to a friendly, actionable
 * message — never a raw JSON blob in the user's face.
 *
 * The backend enforces "cannot be free" at the source (pre-flight balance hold →
 * 403 insufficient_balance; group permission → 403; unpriced model → 400). The
 * Studio mirrors balance to pre-empt, but MUST still translate the server's
 * authoritative rejection if it lands. See:
 *   - backend/internal/handler/openai_images.go:85-127
 *   - backend/internal/handler/openai_gateway_tk_video.go:100-130
 */

export type StudioErrorCode =
  | 'insufficient_balance'
  | 'permission'
  | 'unpriced'
  | 'rate_limited'
  | 'unauthorized'
  | 'generic'

export function classifyGatewayError(message: string | undefined | null): StudioErrorCode {
  const m = (message || '').toLowerCase()
  if (!m) return 'generic'
  if (m.includes('insufficient_balance') || m.includes('insufficient balance')) return 'insufficient_balance'
  if (m.includes('authentication_error') || m.includes('invalid api key') || m.includes('401')) return 'unauthorized'
  if (m.includes('permission') || m.includes('not allowed') || m.includes('forbidden')) return 'permission'
  if (m.includes('not yet priced') || m.includes('unpriced') || m.includes('not priced')) return 'unpriced'
  if (m.includes('429') || m.includes('rate limit') || m.includes('too many requests')) return 'rate_limited'
  return 'generic'
}

/** i18n key for a classified error code (studio.errors.*). */
export function studioErrorI18nKey(code: StudioErrorCode): string {
  return `studio.errors.${code}`
}
