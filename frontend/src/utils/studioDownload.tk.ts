/**
 * TokenKey-only: trigger a browser download of a Studio-generated media URL.
 *
 * `data:` and `blob:` URLs download directly with the given filename. Remote
 * (cross-origin) URLs can't honor the `download` attribute, so we open them in a
 * new tab as a best-effort save target — the user saves from there. Shared by
 * ImageStudio (images) and VideoStudio (videos) so both surfaces behave
 * identically.
 */
import { isInlineStudioVideoUrl } from '@/utils/studioInlineVideo.tk'

export function downloadMedia(url: string, filename: string): void {
  if (!url) return
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    // The download attribute is ignored cross-origin, so for remote URLs fall
    // back to opening in a new tab rather than a same-tab navigation.
    if (!url.startsWith('data:') && !url.startsWith('blob:')) {
      a.target = '_blank'
      a.rel = 'noopener'
    }
    document.body.appendChild(a)
    a.click()
    a.remove()
  } catch {
    window.open(url, '_blank')
  }
}

/** Best-effort clipboard copy of an upstream / inline media URL. */
export async function copyMediaLink(url: string): Promise<boolean> {
  if (!url) return false
  // Inline Veo clips are tab-local data: URIs — they cannot be opened from a
  // pasted browser address bar and blow the clipboard. Callers should download.
  if (isInlineStudioVideoUrl(url)) return false
  try {
    await navigator.clipboard?.writeText(url)
    return true
  } catch {
    return false
  }
}

export type CopyStudioVideoLinkResult = 'copied' | 'inline-unsupported' | 'failed'

/**
 * Copy a shareable video link when the src is http(s); inline clips cannot be
 * shared as URLs (TokenKey is not a media CDN — #944).
 */
export async function copyStudioVideoLink(url: string): Promise<CopyStudioVideoLinkResult> {
  if (!url) return 'failed'
  if (isInlineStudioVideoUrl(url)) return 'inline-unsupported'
  if (await copyMediaLink(url)) return 'copied'
  return 'failed'
}
