/** Protocol shape shared by both windows; verifier and session stay on the Edge. */
export const HANDOFF_MESSAGE = 'tokenkey-edge-handoff-v2'
export function handoffProof(value: unknown): value is string {
  return typeof value === 'string' && /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/.test(value)
}
export function handoffOrigin(raw: string): string {
  const url = new URL(raw)
  if (url.origin !== raw || (url.protocol !== 'https:' && !(url.protocol === 'http:' && (url.hostname === '127.0.0.1' || url.hostname === '[::1]')))) {
    throw new Error('Invalid handoff origin')
  }
  return url.origin
}
export function handoffTargetURL(raw: string): URL {
  const url = new URL(raw)
  handoffOrigin(url.origin)
  if (url.username || url.password || url.pathname !== '/admin/edge-handoff' || url.search || url.hash) throw new Error('Invalid handoff target')
  return url
}
export function handoffRandom(): string {
  return handoffEncode(crypto.getRandomValues(new Uint8Array(32)))
}
export function handoffEncode(value: Uint8Array): string {
  return btoa(String.fromCharCode(...value)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '')
}
