/**
 * TokenKey-only: resolve a generated-video src into a browser-playable URL WITHOUT
 * making TokenKey host the media — the #944 "not a media CDN" rule, shared by every
 * Studio surface that plays video on demand (VideoStudio lightbox, Bake-Off panels).
 *
 * http(s) upstream URLs are handed to the browser as-is. An inline `data:video`
 * result (the one response already delivered to this tab) is converted to a
 * tab-local Blob URL so it plays without re-fetching and without persisting. The
 * returned `revoke()` frees the Blob when playback closes (a no-op for http URLs).
 *
 * Never throws: on any failure (non-base64 data URI, missing atob/createObjectURL,
 * decode error) it falls back to the original src with a no-op revoke, so a caller
 * always gets something usable to hand to a <video :src>.
 */
export interface VideoPlayback {
  /** URL ready for a <video :src> — http(s) upstream URL or a tab-local blob: URL. */
  url: string
  /** Free the Blob URL (no-op for http(s) URLs); call when playback closes. */
  revoke: () => void
}

const NOOP = (): void => {}

export function videoPlaybackUrl(src: string): VideoPlayback {
  if (!src) return { url: '', revoke: NOOP }
  // http(s) (incl. presigned upstream/S3) — hand straight to the browser, no Blob.
  if (!/^data:video/i.test(src)) return { url: src, revoke: NOOP }
  try {
    const match = /^data:(video\/[\w.+-]+);base64,([A-Za-z0-9+/=]+)$/i.exec(src)
    if (!match || typeof atob !== 'function' || typeof URL?.createObjectURL !== 'function') {
      return { url: src, revoke: NOOP }
    }
    const binary = atob(match[2])
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
    const objectUrl = URL.createObjectURL(new Blob([bytes], { type: match[1] }))
    return {
      url: objectUrl,
      revoke: () => {
        try {
          URL.revokeObjectURL?.(objectUrl)
        } catch {
          /* ignore */
        }
      },
    }
  } catch {
    return { url: src, revoke: NOOP }
  }
}
