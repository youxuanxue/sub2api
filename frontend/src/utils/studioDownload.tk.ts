/**
 * TokenKey-only: trigger a browser download of a Studio-generated media URL.
 *
 * `data:` URLs (e.g. base64 images from Vertex) download directly with the given
 * filename. Remote (cross-origin) URLs can't honor the `download` attribute, so
 * we open them in a new tab as a best-effort save target — the user saves from
 * there. Shared by ImageStudio (images) and VideoStudio (videos) so both
 * surfaces behave identically.
 */
export function downloadMedia(url: string, filename: string): void {
  if (!url) return
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    // The download attribute is ignored cross-origin, so for remote URLs fall
    // back to opening in a new tab rather than a same-tab navigation.
    if (!url.startsWith('data:')) {
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
