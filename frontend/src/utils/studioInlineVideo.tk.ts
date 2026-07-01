/**
 * TokenKey-only: parse/normalize inline `data:video/…;base64,…` clips (Veo / Vertex).
 * Shared by extractVideoUrl, videoPlaybackUrl, IndexedDB blob cache, and copy/download.
 *
 * Veo often returns mimeType values like `video/mp4; codecs=avc1.640028`. A naive
 * `data:${mime};base64,` breaks strict parsers that expect `;base64` immediately
 * after the subtype — playback then falls back to mounting the raw multi‑MB data:
 * URI on <video>, which browsers reject. We keep only the base video/* type on the
 * wire and accept optional parameter segments when parsing back.
 */

/** Base video MIME for a data: URI (strip codecs and other parameters). */
export function normalizeVideoMimeForDataUri(claimed: string | undefined, encoding?: string): string {
  const trimmed = (claimed || '').trim()
  if (/^video\/[\w.+-]+/i.test(trimmed)) {
    return trimmed.split(';')[0].trim().toLowerCase()
  }
  const enc = (encoding || '').trim().toLowerCase()
  if (enc === 'base64' || enc === '') return 'video/mp4'
  if (enc.includes('/')) return enc.split(';')[0].trim()
  if (/^(mp4|webm|mov|mkv)$/i.test(enc)) return `video/${enc.toLowerCase()}`
  return 'video/mp4'
}

/** Strip whitespace and normalize URL-safe base64 alphabet before atob(). */
export function normalizeVideoBase64Payload(b64: string): string {
  let s = b64.replace(/\s+/g, '')
  if (s.includes('-') || s.includes('_')) {
    s = s.replace(/-/g, '+').replace(/_/g, '/')
  }
  const pad = s.length % 4
  if (pad) s += '='.repeat(4 - pad)
  return s
}

export function buildDataVideoUri(mimeType: string | undefined, base64: string, encoding?: string): string {
  const mime = normalizeVideoMimeForDataUri(mimeType, encoding)
  const b64 = normalizeVideoBase64Payload(base64)
  if (!b64) return ''
  return `data:${mime};base64,${b64}`
}

export interface ParsedDataVideoUri {
  mime: string
  base64: string
}

/** Parse a data:video URI into mime + raw base64 payload; null when not inline video. */
export function parseDataVideoUri(src: string): ParsedDataVideoUri | null {
  if (!src || !/^data:video/i.test(src)) return null
  const match = /^data:(video\/[\w.+-]+)(?:;[^,]+)*;base64,(.*)$/is.exec(src)
  if (!match) return null
  const base64 = normalizeVideoBase64Payload(match[2])
  if (!base64) return null
  return { mime: match[1].toLowerCase(), base64 }
}

export function isInlineStudioVideoUrl(url: string): boolean {
  return !!url && (/^data:video/i.test(url) || url.startsWith('blob:'))
}
