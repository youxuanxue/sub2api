/**
 * TokenKey-only: classify how a generated video can be replayed / cached in-browser.
 * Surfaces honest labels in Studio so ops/users can tell CORS-blocked upstream URLs
 * from inline data: clips (#944 pass-through — TokenKey is not a media CDN).
 */
export type StudioPlaybackStorage =
  /** data: / blob: — IndexedDB mirror possible in this browser. */
  | 'inline-local'
  /** http(s) and a CORS fetch succeeded — may mirror into IndexedDB. */
  | 'upstream-cors-ok'
  /** http(s) plays in-tab but fetch/cache blocked — refresh loses preview. */
  | 'upstream-cors-blocked'
  /** Stripped / expired / empty url after reload. */
  | 'expired'
  | 'unknown'

export function classifyVideoUrlStorage(url: string): StudioPlaybackStorage {
  if (!url) return 'expired'
  if (/^data:video/i.test(url) || url.startsWith('blob:')) return 'inline-local'
  if (/^https?:\/\//i.test(url)) return 'unknown' // resolved by probeUpstreamVideoCacheability
  return 'unknown'
}

/** Best-effort CORS probe — does NOT download the whole clip (Range bytes=0-0). */
export async function probeUpstreamVideoCacheability(
  url: string
): Promise<'upstream-cors-ok' | 'upstream-cors-blocked'> {
  if (!/^https?:\/\//i.test(url)) return 'upstream-cors-blocked'
  try {
    const res = await fetch(url, {
      method: 'GET',
      mode: 'cors',
      headers: { Range: 'bytes=0-0' },
    })
    if (res.ok || res.status === 206) return 'upstream-cors-ok'
    return 'upstream-cors-blocked'
  } catch {
    return 'upstream-cors-blocked'
  }
}

export async function resolveVideoPlaybackStorage(url: string): Promise<StudioPlaybackStorage> {
  const base = classifyVideoUrlStorage(url)
  if (base === 'unknown' && /^https?:\/\//i.test(url)) {
    return probeUpstreamVideoCacheability(url)
  }
  return base
}

export function studioPlaybackStorageI18nKey(storage: StudioPlaybackStorage | undefined): string {
  switch (storage) {
    case 'inline-local':
      return 'studio.playback.inlineLocal'
    case 'upstream-cors-ok':
      return 'studio.playback.upstreamCorsOk'
    case 'upstream-cors-blocked':
      return 'studio.playback.upstreamCorsBlocked'
    case 'expired':
      return 'studio.playback.expired'
    default:
      return 'studio.playback.unknown'
  }
}
